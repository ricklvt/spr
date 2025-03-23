package internal

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ejoffe/spr/bl/concurrent"
	"github.com/ejoffe/spr/bl/gitapi"
	"github.com/ejoffe/spr/bl/maputils"
	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/git"
	"github.com/ejoffe/spr/github"
	ngit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gogithub "github.com/google/go-github/v69/github"
)

// A PRCommit is a commit its associated Pull Request, and metadata.
type PRCommit struct {
	git.Commit

	// The child of this commit
	Child *PRCommit

	// The parent of this commit.
	Parent *PRCommit

	// The pull request that has this commit at the top
	PullRequest *github.PullRequest

	// The index is a simple way of referring to a commit. Child commits have larger indices.
	Index int

	// The PRIndex is a simple way of referring to a set of Pull Requests. A nil PRIndex indicates that the commit doesn't
	// have a PR (that was created by spr).
	PRIndex *int
}

// Indices is a list of commit indices and the destination pull request set index
type Indices struct {
	DestinationPRIndex *int            // Matches PRCommit.PRIndex
	CommitIndexes      mapset.Set[int] // Matches PRCommit.Index
}

// State holds the state of the local commits and PRs
type State struct {
	// The 0th commit in this slice is the HEAD commit
	Commits       []*PRCommit
	OrphanedPRs   mapset.Set[*github.PullRequest]
	MutatedPRSets mapset.Set[int]
}

type PullRequestStatus struct {
	PullRequest    *gogithub.PullRequest
	CombinedStatus *gogithub.CombinedStatus
	Reviews        []*gogithub.PullRequestReview
}

func indexColor(i *int) string {
	if i == nil {
		return github.ColorBlue
	}
	switch *i % 4 {
	case 0:
		return github.ColorRed
	case 1:
		return github.ColorGreen
	case 2:
		return github.ColorBlue
	case 3:
		return github.ColorLightBlue
	}
	return github.ColorReset
}

func (prc PRCommit) String(config *config.Config) string {
	noPrMessage := "No Pull Request Created"
	tempPrRemainingLen := 36
	empty := github.StatusBitIcons(config)["empty"]

	prString := fmt.Sprintf("[%s%s%s%s] %s%s : %s",
		empty,
		empty,
		empty,
		empty,
		noPrMessage,
		strings.Repeat(" ", tempPrRemainingLen),
		prc.Commit.Subject,
	)

	if prc.PullRequest != nil {
		prString = prc.PullRequest.String(config)
	}

	prIndex := "--"
	if prc.PRIndex != nil {
		prIndex = fmt.Sprintf("s%d", *prc.PRIndex)
	}

	line := fmt.Sprintf("%s%2d%s %s%s%s %s",
		github.ColorLightBlue,
		prc.Index,
		github.ColorReset,
		indexColor(prc.PRIndex),
		prIndex,
		github.ColorReset,
		prString,
	)

	return github.TrimToTerminal(config, line)
}

// Generic function to convert a nil pointer to its zero value.
// Works for any type.
func derefOrDefault[T any](ptr *T) T {
	if ptr == nil {
		var zero T // Zero value of the type T
		return zero
	}
	return *ptr
}

// NewReadState pulls git and github information and constructs the state of the local unmerged commits.
// The resulting State contains the ordered and linked commits along with their associated PRs
func NewReadState(ctx context.Context, config *config.Config, goghclient *gogithub.Client, repo *ngit.Repository) (*State, error) {
	repoOwner := config.Repo.GitHubRepoOwner
	repoName := config.Repo.GitHubRepoName

	gitapi := gitapi.New(config, repo, goghclient)
	gitapi.AppendCommitId()

	prs, _, err := goghclient.PullRequests.List(
		ctx,
		repoOwner,
		repoName,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("getting pull requests for %s/%s: %w", repoOwner, repoName, err)
	}

	prss, err := concurrent.SliceMap(prs, func(pr *gogithub.PullRequest) (PullRequestStatus, error) {
		getCombinedAwait := concurrent.Async5Ret3(
			goghclient.Repositories.GetCombinedStatus,
			ctx, repoOwner, repoName, *pr.Head.SHA, nil,
		)

		prListReviewsAwait := concurrent.Async5Ret3(
			goghclient.PullRequests.ListReviews,
			ctx, repoOwner, repoName, *pr.Number, nil,
		)

		prGetAwait := concurrent.Async4Ret3(
			goghclient.PullRequests.Get,
			ctx, repoOwner, repoName, *pr.Number,
		)

		combinedStatus, _, err := getCombinedAwait.Await()
		if err != nil {
			return PullRequestStatus{}, fmt.Errorf("getting combined status for %s/%s PR:%d: %w", repoOwner, repoName, *pr.Number, err)
		}

		reviews, _, err := prListReviewsAwait.Await()
		if err != nil {
			return PullRequestStatus{}, fmt.Errorf("getting pull request reviews for %s/%s PR:%d: %w", repoOwner, repoName, *pr.Number, err)
		}

		pr, _, err = prGetAwait.Await()
		if err != nil {
			return PullRequestStatus{}, fmt.Errorf("getting pull request details for %s/%s PR:%d: %w", repoOwner, repoName, *pr.Number, err)
		}

		return PullRequestStatus{PullRequest: pr, CombinedStatus: combinedStatus, Reviews: reviews}, nil
	})
	if err != nil {
		return nil, err
	}

	headRef, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("getting repo HEAD %w", err)
	}

	originMainRef, err := gitapi.OriginMainRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting origin main ref %w", err)
	}

	commitIter, err := repo.Log(&ngit.LogOptions{From: headRef.Hash()})
	if err != nil {
		return nil, fmt.Errorf("getting iterator for commits %w", err)
	}

	commits := []*object.Commit{}

	commitIter.ForEach(func(cm *object.Commit) error {
		if originMainRef.Hash().String() == cm.Hash.String() {
			return storer.ErrStop
		}
		commits = append(commits, cm)
		return nil
	})

	return NewState(ctx, config, prss, commits)
}

// NewReadState composes git and github information and constructs the state of the local unmerged commits.
// The resulting State contains the ordered and linked commits along with their associated PRs
func NewState(
	ctx context.Context,
	config *config.Config,
	prss []PullRequestStatus,
	commits []*object.Commit,
) (*State, error) {

	prMap := GeneratePullRequestMap(prss)

	gitCommits := GenerateCommits(commits)
	for _, gitCommit := range gitCommits {
		gitCommit.PullRequest = prMap[gitCommit.CommitID]
	}

	orphanedPRs := AssignPullRequests(config, gitCommits, prMap)

	SetStackedCheck(config, gitCommits)

	return &State{
		Commits:       gitCommits,
		OrphanedPRs:   orphanedPRs,
		MutatedPRSets: mapset.NewSet[int](),
	}, nil
}

func AssignPullRequests(
	config *config.Config,
	gitCommits []*PRCommit,
	prMap map[string]*github.PullRequest,
) mapset.Set[*github.PullRequest] {
	// Add unused PRs to the orphans list
	prGCMap := maputils.NewGC(prMap)
	// Get the mapping of commitIds to PR Set
	prSetMap, ok := config.State.RepoToCommitIdToPRSet[config.Repo.GitHubRepoName]
	if !ok {
		prSetMap = map[string]int{}
	}
	// Purge any mappings that aren't used
	purgeMap := maputils.NewGC(prSetMap)

	for _, gitCommit := range gitCommits {
		if pr, ok := prGCMap.Lookup(gitCommit.CommitID); ok {
			var prIndexPtr *int
			if prIndex, ok := purgeMap.Lookup(gitCommit.CommitID); ok {
				prIndexPtr = &prIndex
			}
			gitCommit.PRIndex = prIndexPtr
			gitCommit.PullRequest = pr
			pr.Commit = gitCommit.Commit
		}
	}

	orphanedPrs := mapset.NewSet[*github.PullRequest]()
	for _, v := range prGCMap.GetUnaccessed() {
		orphanedPrs.Add(v)
	}

	config.State.RepoToCommitIdToPRSet[config.Repo.GitHubRepoName] = purgeMap.PurgeUnaccessed()

	return orphanedPrs
}

func SetStackedCheck(config *config.Config, gitCommits []*PRCommit) {
	for i := len(gitCommits) - 1; i >= 0; i-- {
		cm := gitCommits[i]
		if cm.PullRequest == nil {
			continue
		}
		if cm.WIP {
			return
		}
		if !cm.PullRequest.MergeStatus.NoConflicts {
			return
		}
		if config.Repo.RequireChecks {
			if cm.PullRequest.MergeStatus.ChecksPass != github.CheckStatusPass {
				return
			}
		}
		if config.Repo.RequireApproval {
			if !cm.PullRequest.MergeStatus.ReviewApproved {
				return
			}
		}
		cm.PullRequest.MergeStatus.Stacked = true
	}
}

// Returns the HEAD commit
func (s *State) Head() *PRCommit {
	if len(s.Commits) == 0 {
		return nil
	}
	return s.Commits[0]
}

func (s *State) String() string {
	res := []string{}
	for _, cm := range s.Commits {
		res = append(res, fmt.Sprintf("%p:%#v", cm, *cm))
	}
	return strings.Join(res, ",\n")
}

// ApplyIndices applies the commits in state and updates the State's mutatedPRSets
// The Indices.DestinationPRIndex is update if needed
func (s *State) ApplyIndices(indices *Indices) {
	// If we're assigning 0 commits to a new PR (DestinationPRIndex == nil) then do nothing
	if indices.DestinationPRIndex == nil && indices.CommitIndexes.Cardinality() == 0 {
		return
	}
	// If DestinationPRIndex is null find the next available PR index and update DestinationPRIndex
	if indices.DestinationPRIndex == nil {
		nextDestinationPRIndex := 0
		for _, cm := range s.Commits {
			if cm.PRIndex != nil && *cm.PRIndex >= nextDestinationPRIndex {
				nextDestinationPRIndex = *cm.PRIndex + 1
			}
		}

		indices.DestinationPRIndex = &nextDestinationPRIndex
	}

	// iterate over the commits and update the PRIndex for all matching commitIndex
	// clear the PRs for existing PRs that are in the PRIndex but not in the commitIndex
	for _, cm := range s.Commits {
		shouldBeInPrSet := indices.CommitIndexes.Contains(cm.Index)
		isInPrSet := cm.PRIndex != nil && *cm.PRIndex == *indices.DestinationPRIndex

		// If the commit is already in the PR set and it should be in the PR set then we are done
		if isInPrSet && shouldBeInPrSet {
			continue
		}
		// If the commit is **not** already in the PR set and it should **not** be in the PR set then we are done
		if !isInPrSet && !shouldBeInPrSet {
			continue
		}
		// If the commit is already in the PR set and it should **not** be then we need to clear the PR Index
		if isInPrSet && !shouldBeInPrSet {
			s.OrphanedPRs.Add(cm.PullRequest)
			s.MutatedPRSets.Add(*indices.DestinationPRIndex)
			cm.PRIndex = nil
			continue
		}

		// If the commit is **not** already in the PR set and it should be then we need to set PR Index
		if !isInPrSet && shouldBeInPrSet {
			// If we are replacing another PR then both the old and the new PR sets were mutated
			if cm.PRIndex != nil {
				s.MutatedPRSets.Add(*cm.PRIndex)
			}
			s.MutatedPRSets.Add(*indices.DestinationPRIndex)
			cm.PRIndex = indices.DestinationPRIndex
		}
	}

	// It is possible to mutate a PR set out of existence. So purge any in the MutatedPRSets that no longer exist.
	existingPRSets := mapset.NewSet[int]()
	for _, cm := range s.Commits {
		if cm.PRIndex == nil {
			continue
		}
		existingPRSets.Add(*cm.PRIndex)
	}
	s.MutatedPRSets = s.MutatedPRSets.Intersect(existingPRSets)
}

// CommitsByPRSet returns all of the commits for the given PR set with the newest commits first.
// Note that the Parent, Child, Index fields are not changed in the returned PRCommits
func (s *State) CommitsByPRSet(prIndex int) []*PRCommit {
	var commits []*PRCommit
	for _, ci := range s.Commits {
		if ci.PRIndex == nil {
			continue
		}

		if *ci.PRIndex == prIndex {
			commits = append(commits, ci)
		}
	}

	return commits
}

// MutatedPRSetsWithOutOfOrderCommits returns the PRSets where the commits are out of order and the PRs need to be rebuilt.
func (s *State) MutatedPRSetsWithOutOfOrderCommits() mapset.Set[int] {
	outOfOrderPRSets := mapset.NewSet[int]()
	for prSet := range s.MutatedPRSets.Iter() {
		lastTo := ""

		for _, commit := range s.Commits {
			// If the commit doesn't have a PR then we can ignore it.
			if commit.PullRequest == nil {
				continue
			}
			// Same as above.
			if commit.PRIndex == nil {
				continue
			}
			// If the commit is a part of a different PR set then ignore it.
			if *commit.PRIndex != prSet {
				continue
			}

			if lastTo == "" {
				lastTo = commit.PullRequest.ToBranch
				continue
			}
			if commit.PullRequest.FromBranch != lastTo {
				outOfOrderPRSets.Add(prSet)
				break
			}
			lastTo = commit.PullRequest.ToBranch
		}
	}
	return outOfOrderPRSets
}

// PullRequest gets all pull request from the PRCommits.
func PullRequests(commits []*PRCommit) []*github.PullRequest {
	pullRequests := make([]*github.PullRequest, 0, len(commits))
	for _, ci := range commits {
		if ci.PullRequest != nil {
			pullRequests = append(pullRequests, ci.PullRequest)
		}
	}
	return pullRequests
}

func GeneratePullRequestMap(prss []PullRequestStatus) map[string]*github.PullRequest {
	if prss == nil {
		return nil
	}

	// Map of commitId -> github.PullRequests
	prMap := map[string]*github.PullRequest{}

	for _, prs := range prss {
		pr := prs.PullRequest
		commitId := CommitIdFromBranch(*pr.Head.Ref)
		if commitId == "" {
			continue
		}
		fromBranch := derefOrDefault(derefOrDefault(pr.Head).Ref)
		toBranch := derefOrDefault(derefOrDefault(pr.Base).Ref)
		ghpr := &github.PullRequest{
			ID:          fmt.Sprintf("%d", *pr.ID),
			Number:      derefOrDefault(pr.Number),
			FromBranch:  fromBranch,
			ToBranch:    toBranch,
			Title:       derefOrDefault(pr.Title),
			Body:        derefOrDefault(pr.Body),
			MergeStatus: ComputeMergeStatus(prs),
		}
		prMap[commitId] = ghpr
	}

	return prMap
}

func CommitIdFromBranch(branchName string) string {
	segments := strings.Split(branchName, "/")
	if len(segments) != 3 {
		return ""
	}
	if segments[0] != "spr" {
		return ""
	}
	commitId := segments[2]
	if len(commitId) != 8 {
		return ""
	}
	return commitId
}

func ComputeMergeStatus(prs PullRequestStatus) github.PullRequestMergeStatus {
	prms := github.PullRequestMergeStatus{}
	if prs.CombinedStatus == nil || prs.CombinedStatus.State == nil {
		prms.ChecksPass = github.CheckStatusUnknown
	} else if prs.CombinedStatus.TotalCount != nil && *prs.CombinedStatus.TotalCount == 0 {
		prms.ChecksPass = github.CheckStatusPass
	} else if *prs.CombinedStatus.State == "success" {
		prms.ChecksPass = github.CheckStatusPass
	} else if *prs.CombinedStatus.State == "pending" {
		prms.ChecksPass = github.CheckStatusPending
	} else if *prs.CombinedStatus.State == "failure" {
		prms.ChecksPass = github.CheckStatusFail
	}

	prms.NoConflicts = prs.PullRequest != nil && prs.PullRequest.Mergeable != nil && *prs.PullRequest.Mergeable

	for _, review := range prs.Reviews {
		if review.State != nil && *review.State == "APPROVED" {
			prms.ReviewApproved = true
			break
		}
	}

	return prms
}

func GenerateCommits(commits []*object.Commit) []*PRCommit {
	gitCommits := make([]*PRCommit, 0, len(commits))

	// Make sure that commits are always stored HEAD first.
	commits = HeadFirst(commits)

	var child *PRCommit
	for i, cm := range commits {
		commitId := CommitId(cm.Message)

		c := &PRCommit{
			Commit: git.Commit{
				CommitID:   commitId,
				CommitHash: cm.Hash.String(),
				Subject:    Subject(cm.Message),
				Body:       Body(cm.Message),
				WIP:        IsWIP(cm.Message),
			},
			Child:       child,
			Parent:      nil,
			PullRequest: nil,
			Index:       len(commits) - (i + 1),
			PRIndex:     nil,
		}
		// Point the previous one to us
		if child != nil {
			child.Parent = c
		}
		gitCommits = append(gitCommits, c)
		child = c
	}

	return gitCommits
}

// UpdatePRSetState updates the RepoToCommitIdToPRSet in the config based upon the state.Commits
func (s *State) UpdatePRSetState(config *config.Config) {
	// It is simpler to just build up a new map for this repo than to mutate the existing map
	prSetMap := map[string]int{}

	for _, commit := range s.Commits {
		if commit.PRIndex == nil {
			continue
		}
		prSetMap[commit.CommitID] = *commit.PRIndex

	}
	config.State.RepoToCommitIdToPRSet[config.Repo.GitHubRepoName] = prSetMap
}

func HeadFirst(commits []*object.Commit) []*object.Commit {
	if len(commits) < 2 {
		return commits
	}

	// See if the second is listed as the firsts parent if so we are in the right order
	for _, firstParents := range commits[0].ParentHashes {
		if commits[1].Hash.String() == firstParents.String() {
			return commits
		}
	}
	slices.Reverse(commits)
	return commits
}

var commitIDRegex = regexp.MustCompile(`(?m)^commit-id\:([a-f0-9]{8})$`)

// CommitId parses out the commit id from "commit-id:00000000"
func CommitId(msg string) string {
	matches := commitIDRegex.FindStringSubmatch(msg)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// IsWIP returns true if the message starts with "WIP"
func IsWIP(msg string) bool {
	return strings.HasPrefix(msg, "WIP")
}

// Subject returns the first line of the message
func Subject(msg string) string {
	return strings.SplitN(msg, "\n", 2)[0]
}

// Subject returns all but the first line of the message
func Body(msg string) string {
	res := strings.SplitN(msg, "\n", 2)
	if len(res) < 2 {
		return ""
	}
	return res[1]
}
