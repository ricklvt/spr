package spr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/ejoffe/profiletimer"
	"github.com/ejoffe/rake"
	"github.com/ejoffe/spr/bl"
	"github.com/ejoffe/spr/bl/concurrent"
	"github.com/ejoffe/spr/bl/gitapi"
	"github.com/ejoffe/spr/bl/selector"
	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/config/config_parser"
	"github.com/ejoffe/spr/git"
	"github.com/ejoffe/spr/github"
	ngit "github.com/go-git/go-git/v5"
	gogithub "github.com/google/go-github/v69/github"
)

// NewStackedPR constructs and returns a new stackediff instance.
func NewStackedPR(config *config.Config, github github.GitHubInterface, gitcmd git.GitInterface, repo *ngit.Repository, goghclient *gogithub.Client) *stackediff {

	return &stackediff{
		config:       config,
		github:       github,
		gitcmd:       gitcmd,
		repo:         repo,
		goghclient:   goghclient,
		profiletimer: profiletimer.StartNoopTimer(),

		output: os.Stdout,
		input:  os.Stdin,
	}
}

type stackediff struct {
	config        *config.Config
	github        github.GitHubInterface
	gitcmd        git.GitInterface
	repo          *ngit.Repository
	goghclient    *gogithub.Client
	profiletimer  profiletimer.Timer
	DetailEnabled bool

	output       io.Writer
	input        io.Reader
	synchronized bool // When true code is executed without goroutines. Allows test to be deterministic
}

// AmendCommit enables one to easily amend a commit in the middle of a stack
//
//	of commits. A list of commits is printed and one can be chosen to be amended.
func (sd *stackediff) AmendCommit(ctx context.Context) {
	localCommits := git.GetLocalCommitStack(sd.config, sd.gitcmd)
	if len(localCommits) == 0 {
		fmt.Fprintf(sd.output, "No commits to amend\n")
		return
	}

	for i := len(localCommits) - 1; i >= 0; i-- {
		commit := localCommits[i]
		fmt.Fprintf(sd.output, " %d : %s : %s\n", i+1, commit.CommitID[0:8], commit.Subject)
	}

	if len(localCommits) == 1 {
		fmt.Fprintf(sd.output, "Commit to amend (%d): ", 1)
	} else {
		fmt.Fprintf(sd.output, "Commit to amend (%d-%d): ", 1, len(localCommits))
	}

	reader := bufio.NewReader(sd.input)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	commitIndex, err := strconv.Atoi(line)
	if err != nil || commitIndex < 1 || commitIndex > len(localCommits) {
		fmt.Fprint(sd.output, "Invalid input\n")
		return
	}
	commitIndex = commitIndex - 1
	check(err)
	sd.gitcmd.MustGit("commit --fixup "+localCommits[commitIndex].CommitHash, nil)

	rebaseCmd := fmt.Sprintf("rebase -i --autosquash --autostash %s/%s",
		sd.config.Repo.GitHubRemote, sd.config.Repo.GitHubBranch)
	sd.gitcmd.MustGit(rebaseCmd, nil)
}

func (sd *stackediff) addReviewers(ctx context.Context,
	pr *github.PullRequest, reviewers []string, assignable []github.RepoAssignee) {
	userIDs := make([]string, 0, len(reviewers))
	for _, r := range reviewers {
		found := false
		for _, u := range assignable {
			if strings.EqualFold(r, u.Login) {
				found = true
				userIDs = append(userIDs, u.ID)
				break
			}
		}
		if !found {
			check(fmt.Errorf("unable to add reviewer, user %q not found", r))
		}
	}
	sd.github.AddReviewers(ctx, pr, userIDs)
}

func alignLocalCommits(commits []git.Commit, prs []*github.PullRequest) []git.Commit {
	var remoteCommits = map[string]bool{}
	for _, pr := range prs {
		for _, c := range pr.Commits {
			remoteCommits[c.CommitID] = c.CommitID == pr.Commit.CommitID
		}
	}

	var result []git.Commit
	for _, commit := range commits {
		if head, ok := remoteCommits[commit.CommitID]; ok && !head {
			continue
		}

		result = append(result, commit)
	}

	return result
}

// UpdatePullRequests implements a stacked diff workflow on top of github.
//
//	Each time it's called it compares the local branch unmerged commits
//	 with currently open pull requests in github.
//	It will create a new pull request for all new commits, and update the
//	 pull request if a commit has been amended.
//	In the case where commits are reordered, the corresponding pull requests
//	 will also be reordered to match the commit stack order.
func (sd *stackediff) UpdatePullRequests(ctx context.Context, reviewers []string, count *uint) {
	sd.profiletimer.Step("UpdatePullRequests::Start")
	githubInfo := sd.fetchAndGetGitHubInfo(ctx)
	if githubInfo == nil {
		return
	}
	sd.profiletimer.Step("UpdatePullRequests::FetchAndGetGitHubInfo")
	localCommits := alignLocalCommits(git.GetLocalCommitStack(sd.config, sd.gitcmd), githubInfo.PullRequests)
	sd.profiletimer.Step("UpdatePullRequests::GetLocalCommitStack")

	// close prs for deleted commits
	var validPullRequests []*github.PullRequest
	localCommitMap := map[string]*git.Commit{}
	for _, commit := range localCommits {
		localCommitMap[commit.CommitID] = &commit
	}
	for _, pr := range githubInfo.PullRequests {
		if _, found := localCommitMap[pr.Commit.CommitID]; !found {
			sd.github.CommentPullRequest(ctx, pr, "Closing pull request: commit has gone away")
			sd.github.ClosePullRequest(ctx, pr)
		} else {
			validPullRequests = append(validPullRequests, pr)
		}
	}
	githubInfo.PullRequests = validPullRequests

	if commitsReordered(localCommits, githubInfo.PullRequests) {
		wg := new(sync.WaitGroup)
		wg.Add(len(githubInfo.PullRequests))

		// if commits have been reordered :
		//   first - rebase all pull requests to target branch
		//   then - update all pull requests
		for i := range githubInfo.PullRequests {
			fn := func(i int) {
				pr := githubInfo.PullRequests[i]
				sd.github.UpdatePullRequest(ctx, sd.gitcmd, githubInfo.PullRequests, pr, pr.Commit, nil)
				wg.Done()
			}
			if sd.synchronized {
				fn(i)
			} else {
				go fn(i)
			}
		}

		wg.Wait()
		sd.profiletimer.Step("UpdatePullRequests::ReparentPullRequestsToMaster")
	}

	if !sd.syncCommitStackToGitHub(ctx, localCommits, githubInfo) {
		return
	}
	sd.profiletimer.Step("UpdatePullRequests::SyncCommitStackToGithub")

	type prUpdate struct {
		pr         *github.PullRequest
		commit     git.Commit
		prevCommit *git.Commit
	}

	updateQueue := make([]prUpdate, 0)
	var assignable []github.RepoAssignee

	// iterate through local_commits and update pull_requests
	var prevCommit *git.Commit
	for commitIndex, c := range localCommits {
		if c.WIP {
			break
		}
		prFound := false
		for _, pr := range githubInfo.PullRequests {
			if c.CommitID == pr.Commit.CommitID {
				prFound = true
				updateQueue = append(updateQueue, prUpdate{pr, c, prevCommit})
				pr.Commit = c
				if len(reviewers) != 0 {
					fmt.Fprintf(sd.output, "warning: not updating reviewers for PR #%d\n", pr.Number)
				}
				prevCommit = &localCommits[commitIndex]
				break
			}
		}
		if !prFound {
			// if pull request is not found for this commit_id it means the commit
			//  is new and we need to create a new pull request
			pr := sd.github.CreatePullRequest(ctx, sd.gitcmd, githubInfo, c, prevCommit)
			githubInfo.PullRequests = append(githubInfo.PullRequests, pr)
			updateQueue = append(updateQueue, prUpdate{pr, c, prevCommit})
			if len(reviewers) != 0 {
				if assignable == nil {
					assignable = sd.github.GetAssignableUsers(ctx)
				}
				sd.addReviewers(ctx, pr, reviewers, assignable)
			}
			prevCommit = &localCommits[commitIndex]
		}

		if count != nil && (commitIndex+1) == int(*count) {
			break
		}
	}
	sd.profiletimer.Step("UpdatePullRequests::updatePullRequests")

	wg := new(sync.WaitGroup)
	wg.Add(len(updateQueue))

	// Sort the PR stack by the local commit order, in case some commits were reordered
	sortedPullRequests := sortPullRequestsByLocalCommitOrder(githubInfo.PullRequests, localCommits)
	for i := range updateQueue {
		fn := func(i int) {
			pr := updateQueue[i]
			sd.github.UpdatePullRequest(ctx, sd.gitcmd, sortedPullRequests, pr.pr, pr.commit, pr.prevCommit)
			wg.Done()
		}
		if sd.synchronized {
			fn(i)
		} else {
			go fn(i)
		}
	}

	wg.Wait()

	sd.profiletimer.Step("UpdatePullRequests::commitUpdateQueue")

	sd.StatusPullRequests(ctx)
}

// MergePullRequests will go through all the current pull requests
//
//	and merge all requests that are mergeable.
//
// For a pull request to be mergeable it has to:
//   - have at least one approver
//   - pass all checks
//   - have no merge conflicts
//   - not be on top of another unmergable request
//   - pass merge checks (using 'spr check') if configured
//
// In order to merge a stack of pull requests without generating conflicts
//
//	and other pr issues. We find the top mergeable pull request in the stack,
//	than we change this pull request's base to be master and then merge the
//	pull request. This one merge in effect merges all the commits in the stack.
//	We than close all the pull requests which are below the merged request, as
//	their commits have already been merged.
func (sd *stackediff) MergePullRequests(ctx context.Context, count *uint) {
	sd.profiletimer.Step("MergePullRequests::Start")
	githubInfo := sd.github.GetInfo(ctx, sd.gitcmd)
	sd.profiletimer.Step("MergePullRequests::getGitHubInfo")

	// MergeCheck
	if sd.config.Repo.MergeCheck != "" {
		localCommits := git.GetLocalCommitStack(sd.config, sd.gitcmd)
		if len(localCommits) > 0 {
			lastCommit := localCommits[len(localCommits)-1]
			checkedCommit, found := sd.config.State.MergeCheckCommit[githubInfo.Key()]

			if !found {
				check(errors.New("need to run merge check 'spr check' before merging"))
			} else if checkedCommit != "SKIP" && lastCommit.CommitHash != checkedCommit {
				check(errors.New("need to run merge check 'spr check' before merging"))
			}
		}
	}

	// Figure out top most pr in the stack that is mergeable
	var prIndex int
	for prIndex = 0; prIndex < len(githubInfo.PullRequests); prIndex++ {
		pr := githubInfo.PullRequests[prIndex]
		if !pr.Mergeable(sd.config) {
			prIndex--
			break
		}
		if count != nil && (prIndex+1) == int(*count) {
			break
		}
	}
	if prIndex == len(githubInfo.PullRequests) {
		prIndex--
	}
	if prIndex == -1 {
		return
	}
	prToMerge := githubInfo.PullRequests[prIndex]

	// Update the base of the merging pr to target branch
	sd.github.UpdatePullRequest(ctx, sd.gitcmd, githubInfo.PullRequests, prToMerge, prToMerge.Commit, nil)
	sd.profiletimer.Step("MergePullRequests::update pr base")

	// Merge pull request
	mergeMethod, err := sd.config.MergeMethod()
	check(err)
	sd.github.MergePullRequest(ctx, prToMerge, mergeMethod)
	if sd.config.User.DeleteMergedBranches {
		git.DeleteRemoteBranch(sd.config, sd.gitcmd, prToMerge.FromBranch)
	}

	// Close all the pull requests in the stack below the merged pr
	//  Before closing add a review comment with the pr that merged the commit.
	for i := 0; i < prIndex; i++ {
		pr := githubInfo.PullRequests[i]
		comment := fmt.Sprintf(
			"✓ Commit merged in pull request [#%d](https://%s/%s/%s/pull/%d)",
			prToMerge.Number, sd.config.Repo.GitHubHost, sd.config.Repo.GitHubRepoOwner, sd.config.Repo.GitHubRepoName, prToMerge.Number)
		sd.github.CommentPullRequest(ctx, pr, comment)
		sd.github.ClosePullRequest(ctx, pr)
		if sd.config.User.DeleteMergedBranches {
			git.DeleteRemoteBranch(sd.config, sd.gitcmd, pr.FromBranch)
		}
	}
	sd.profiletimer.Step("MergePullRequests::close prs")

	for i := 0; i <= prIndex; i++ {
		pr := githubInfo.PullRequests[i]
		pr.Merged = true
		fmt.Fprintf(sd.output, "%s\n", pr.String(sd.config))
	}

	sd.profiletimer.Step("MergePullRequests::End")
}

// MergePRSet merges the given PR set
// In order to merge a PRSet without conflicts we find the newest PR and update the PR to merge into main/master.
// The newest PR branch has all of the commits of the others so this will land all commits into main/master.
// We then close the other PRs.
func (sd *stackediff) MergePRSet(ctx context.Context, setIndex string) {
	sd.profiletimer.Step("MergePRSet::Start")
	gitapi := gitapi.New(sd.config, sd.repo, sd.goghclient)

	index, ok := selector.AsPRSet(setIndex)
	if !ok {
		check(fmt.Errorf("unable to parse PR set index %s", setIndex))
	}
	sd.profiletimer.Step("MergePRSet::AsPRSet")

	// Merge the newest commit into main as it has all of the commits.
	// Close the remaining commits
	state, err := bl.NewReadState(ctx, sd.config, sd.goghclient, sd.repo)
	check(err)
	sd.profiletimer.Step("MergePRSet::NewReadState")

	// MergeCheck
	if sd.config.Repo.MergeCheck != "" {
		sd.profiletimer.Step("MergePRSet::MergeCheck")
		commits := state.CommitsByPRSet(index)
		if len(commits) > 0 {
			sd.profiletimer.Step("MergePRSet::GetInfo")
			githubInfo := sd.github.GetInfo(ctx, sd.gitcmd)
			sd.profiletimer.Step("MergePRSet::GotInfo")
			// Get the newest commit
			lastCommit := state.CommitsByPRSet(index)[0]
			checkedCommit, found := sd.config.State.MergeCheckCommit[githubInfo.Key()]

			if !found {
				check(errors.New("need to run merge check 'spr check' before merging"))
			} else if checkedCommit != "SKIP" && lastCommit.CommitHash != checkedCommit {
				check(errors.New("need to run merge check 'spr check' before merging"))
			}
			sd.profiletimer.Step("MergePRSet::MergeChecked")
		}
	}

	commits := state.CommitsByPRSet(index)
	// We want the oldest PR first so we preserve the PR links when updating it to merge to main/master
	slices.Reverse(commits)
	pullRequests := bl.PullRequests(commits)
	_, err = concurrent.SliceMapWithIndex(commits, func(cindex int, ci *bl.PRCommit) (struct{}, error) {
		if cindex == len(commits)-1 {
			err := gitapi.UpdatePullRequestToMain(ctx, pullRequests, ci.PullRequest, ci.Commit)
			if err != nil {
				return struct{}{}, fmt.Errorf("update PR to merge to main in preparation to merge PR set %w", err)
			}

			err = gitapi.MergePullRequest(ctx, ci.PullRequest)
			if err != nil {
				return struct{}{}, fmt.Errorf("unable to merge oldest PR in PR set %w", err)
			}

			err = sd.repo.Fetch(&ngit.FetchOptions{
				RemoteName: sd.config.Repo.GitHubRemote,
				Prune:      true,
			})
			if err != nil {
				return struct{}{}, fmt.Errorf("unable to fetch merge changes %w", err)
			}
		}

		// Delete/close all pull requests
		err := gitapi.DeletePullRequest(ctx, ci.PullRequest)
		if err != nil {
			return struct{}{}, fmt.Errorf("unable to close non-oldest PR in PR set %w", err)
		}
		return struct{}{}, err
	})
	check(err)

	sd.profiletimer.Step("MergePRSet::NewReadState")
}

// UpdatePRSets updatest the PR Sets given the selection.
//   - The PRs are created in order so the oldest commit in the PR Set is created first.
//   - If there are more than one PR in a PR set an index is included in the PR message showing the other PRs in the PR set
//     with an arrow pointing to where you are.
//   - If a new PR set overlaps with an existing one. The overlapped commits are pulled into the new PR set.
func (sd *stackediff) UpdatePRSets(ctx context.Context, sel string) {
	sd.profiletimer.Step("UpdatePRSets::Start")
	gitapi := gitapi.New(sd.config, sd.repo, sd.goghclient)

	// Add the commit-id to any commits that don't have it yet.
	gitapi.AppendCommitId()
	sd.profiletimer.Step("UpdatePRSets::AppndCommitId")

	// Fetch/Prune from github remote
	awaitFetch := concurrent.Async1Ret1(
		sd.repo.Fetch,
		&ngit.FetchOptions{
			RemoteName: sd.config.Repo.GitHubRemote,
			Prune:      true,
		},
	)

	state, err := bl.NewReadState(ctx, sd.config, sd.goghclient, sd.repo)
	check(err)
	sd.profiletimer.Step("UpdatePRSets::NewReadState")

	// Compute the indices that will be included in the updated PR
	indices, err := selector.Evaluate(state.Commits, sel)
	check(err)
	sd.profiletimer.Step("UpdatePRSets::Evaluate")

	// Update the commits PRIndex and tracked orphaned and mutated PR sets.
	// Sets the indices.DestinationPRIndex if a new destination PRIndex is created
	state.ApplyIndices(&indices)
	sd.profiletimer.Step("UpdatePRSets::ApplyIndices")

	// Delete orphaned PRs (along with the associated branches)
	_, err = concurrent.SliceMap(state.OrphanedPRs.ToSlice(), func(pr *github.PullRequest) (struct{}, error) {
		if pr == nil {
			return struct{}{}, nil
		}
		err := gitapi.DeletePullRequest(ctx, pr)
		return struct{}{}, err
	})
	check(err)
	state.OrphanedPRs.Clear()
	sd.profiletimer.Step("UpdatePRSets::DeleteOrphanedPRs")

	// Handle reordered commits.
	// There are two challenges when commits are reordered.
	// One is that we try and update the branches first and during that process we create a situation where the
	// target branch already has the source so the PR gets automatically closed.
	// The second is that we update the to/from branches within the PR but we end up with a situation where the dest branch has all (or more) commits from the
	// source. Which gets rejected by github.
	// The solution is to overwrite all branches so they merge to main from whatever. Then push the branches, the re-update
	// the PRs
	for prSet := range state.MutatedPRSetsWithOutOfOrderCommits().Iter() {
		commits := state.CommitsByPRSet(prSet)
		// We want the oldest first so we create PRs for it first
		slices.Reverse(commits)
		pullRequests := bl.PullRequests(commits)
		_, err = concurrent.SliceMapWithIndex(commits, func(cindex int, ci *bl.PRCommit) (struct{}, error) {
			// Don't need to rework if no PR exists
			if ci.PullRequest == nil {
				return struct{}{}, err
			}

			err := gitapi.UpdatePullRequestToMain(ctx, pullRequests, ci.PullRequest, ci.Commit)
			return struct{}{}, err
		})
	}
	sd.profiletimer.Step("UpdatePRSets::HandleRedorderdCommits")

	// Wait for the fetch/prune to complete
	err = awaitFetch.Await()
	check(err)
	sd.profiletimer.Step("UpdatePRSets::Fetch")

	// Update all branches of the mutated PR sets
	for prSet := range state.MutatedPRSets.Iter() {
		commits := state.CommitsByPRSet(prSet)
		// Destination branch starts with the "main" branch.
		destBranchName := sd.config.Repo.GitHubBranch

		// The first cid is the top (committed last) we need to create the branch for the last one first as that is what
		// will be merged into the main branch first then build on that one for the subsequent commits.
		for c := len(commits) - 1; c >= 0; c-- {
			branchName := git.BranchNameFromCommitId(sd.config, commits[c].CommitID)

			err := gitapi.CreateRemoteBranchWithCherryPick(ctx, branchName, destBranchName, commits[c].CommitHash)
			check(err)

			destBranchName = branchName
		}
	}
	sd.profiletimer.Step("UpdatePRSets::UpdateAllBranches")

	// Update PR sets for all impacted mutated PR sets.
	for prSet := range state.MutatedPRSets.Iter() {
		commits := state.CommitsByPRSet(prSet)
		// We want the oldest first so we create PRs for it first
		slices.Reverse(commits)

		// Create new PRs that are missing for the impacted PR set
		// For now we just create a PR simple PR without linking PRs to each other as there could be other missing PRs so we
		// can't link to a missing PR. Once all PRs have been created we will update them.
		// We don't want to do this in parallel as we want the PR numbers to be sequential starting with the oldest first.
		for cindex, ci := range commits {
			if ci.PullRequest != nil {
				continue
			}
			var parentBaseCommit *git.Commit
			if cindex != 0 {
				parentBaseCommit = &commits[cindex-1].Commit
			}

			pr, err := gitapi.CreatePullRequest(ctx, ci.Commit, parentBaseCommit)
			check(err)
			ci.PullRequest = pr
		}

		// All commits should now have PRs
		pullRequests := bl.PullRequests(commits)

		_, err = concurrent.SliceMapWithIndex(commits, func(cindex int, ci *bl.PRCommit) (struct{}, error) {
			var parentBaseCommit *git.Commit
			if cindex != 0 {
				parentBaseCommit = &commits[cindex-1].Commit
			}
			err := gitapi.UpdatePullRequest(ctx, pullRequests, ci.PullRequest, ci.Commit, parentBaseCommit)
			return struct{}{}, err
		})
	}
	sd.profiletimer.Step("UpdatePRSets::Update/CreatePRSets")

	// Update persistent PR set state
	state.UpdatePRSetState(sd.config)
	sd.profiletimer.Step("UpdatePRSets::UpdatePRSetState")

	// Display status
	sd.StatusCommitsAndPRSets(ctx)
}

// StatusCommitsAndPRSets outputs the status of all commits and PR sets.
// If a PR set is stored in state but no PR exists (like it was manually deleted from the github UI) then it will be
// removed from state.
func (sd *stackediff) StatusCommitsAndPRSets(ctx context.Context) {
	sd.profiletimer.Step("StatusCommitsAndPRSets::Start")
	state, err := bl.NewReadState(ctx, sd.config, sd.goghclient, sd.repo)
	check(err)
	sd.profiletimer.Step("StatusCommitsAndPRSets::NewReadState")

	if state.Head() == nil {
		fmt.Fprintf(sd.output, "no local commits\n")
		return
	}
	if sd.DetailEnabled {
		fmt.Fprint(sd.output, header(sd.config))
	}
	sd.profiletimer.Step("StatusCommitsAndPRSets::PrintDetails")
	for this := state.Head(); this != nil; this = this.Parent {
		fmt.Fprintf(sd.output, "%s\n", this.String(sd.config))
	}
	sd.profiletimer.Step("StatusCommitsAndPRSets::OutputStatus")
}

// StatusPullRequests fetches all the users pull requests from github and
//
//	prints out the status of each. It does not make any updates locally or
//	remotely on github.
func (sd *stackediff) StatusPullRequests(ctx context.Context) {
	sd.profiletimer.Step("StatusPullRequests::Start")
	githubInfo := sd.github.GetInfo(ctx, sd.gitcmd)

	if len(githubInfo.PullRequests) == 0 {
		fmt.Fprintf(sd.output, "pull request stack is empty\n")
	} else {
		if sd.DetailEnabled {
			fmt.Fprint(sd.output, header(sd.config))
		}
		for i := len(githubInfo.PullRequests) - 1; i >= 0; i-- {
			pr := githubInfo.PullRequests[i]
			fmt.Fprintf(sd.output, "%s\n", pr.String(sd.config))
		}
	}
	sd.profiletimer.Step("StatusPullRequests::End")
}

// SyncStack synchronizes your local stack with remote's
func (sd *stackediff) SyncStack(ctx context.Context) {
	sd.profiletimer.Step("SyncStack::Start")
	defer sd.profiletimer.Step("SyncStack::End")

	githubInfo := sd.github.GetInfo(ctx, sd.gitcmd)

	if len(githubInfo.PullRequests) == 0 {
		fmt.Fprintf(sd.output, "pull request stack is empty\n")
		return
	}

	lastPR := githubInfo.PullRequests[len(githubInfo.PullRequests)-1]
	syncCommand := fmt.Sprintf("cherry-pick ..%s", lastPR.Commit.CommitHash)
	err := sd.gitcmd.Git(syncCommand, nil)
	check(err)
}

func (sd *stackediff) RunMergeCheck(ctx context.Context) {
	sd.profiletimer.Step("RunMergeCheck::Start")
	defer sd.profiletimer.Step("RunMergeCheck::End")

	if sd.config.Repo.MergeCheck == "" {
		fmt.Println("use MergeCheck to configure a pre merge check command to run")
		return
	}

	localCommits := git.GetLocalCommitStack(sd.config, sd.gitcmd)
	if len(localCommits) == 0 {
		fmt.Println("no local commits - nothing to check")
		return
	}

	githubInfo := sd.github.GetInfo(ctx, sd.gitcmd)

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigch)

	var cmd *exec.Cmd
	splitCmd := strings.Split(sd.config.Repo.MergeCheck, " ")
	if len(splitCmd) == 1 {
		cmd = exec.Command(splitCmd[0])
	} else {
		cmd = exec.Command(splitCmd[0], splitCmd[1:]...)
	}
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	check(err)

	go func() {
		_, ok := <-sigch
		if ok {
			err := cmd.Process.Signal(syscall.SIGKILL)
			check(err)
		}
	}()

	err = cmd.Wait()

	if err != nil {
		sd.config.State.MergeCheckCommit[githubInfo.Key()] = ""
		rake.LoadSources(sd.config.State,
			rake.YamlFileWriter(config_parser.InternalConfigFilePath()))
		fmt.Printf("MergeCheck FAILED: %s\n", err)
		return
	}

	lastCommit := localCommits[len(localCommits)-1]
	sd.config.State.MergeCheckCommit[githubInfo.Key()] = lastCommit.CommitHash
	rake.LoadSources(sd.config.State,
		rake.YamlFileWriter(config_parser.InternalConfigFilePath()))
	fmt.Println("MergeCheck PASSED")
}

// ProfilingEnable enables stopwatch profiling
func (sd *stackediff) ProfilingEnable() {
	sd.profiletimer = profiletimer.StartProfileTimer()
}

// ProfilingSummary prints profiling info to stdout
func (sd *stackediff) ProfilingSummary() {
	err := sd.profiletimer.ShowResults()
	check(err)
}

func commitsReordered(localCommits []git.Commit, pullRequests []*github.PullRequest) bool {
	for i := 0; i < len(pullRequests); i++ {
		if localCommits[i].CommitID != pullRequests[i].Commit.CommitID {
			return true
		}
	}
	return false
}

func sortPullRequestsByLocalCommitOrder(pullRequests []*github.PullRequest, localCommits []git.Commit) []*github.PullRequest {
	pullRequestMap := map[string]*github.PullRequest{}
	for _, pullRequest := range pullRequests {
		pullRequestMap[pullRequest.Commit.CommitID] = pullRequest
	}

	var sortedPullRequests []*github.PullRequest
	for _, commit := range localCommits {
		if !commit.WIP && pullRequestMap[commit.CommitID] != nil {
			sortedPullRequests = append(sortedPullRequests, pullRequestMap[commit.CommitID])
		}
	}
	return sortedPullRequests
}

func (sd *stackediff) fetchAndGetGitHubInfo(ctx context.Context) *github.GitHubInfo {
	if sd.config.Repo.ForceFetchTags {
		sd.gitcmd.MustGit("fetch --tags --force", nil)
	} else {
		sd.gitcmd.MustGit("fetch", nil)
	}
	rebaseCommand := fmt.Sprintf("rebase %s/%s --autostash",
		sd.config.Repo.GitHubRemote, sd.config.Repo.GitHubBranch)
	err := sd.gitcmd.Git(rebaseCommand, nil)
	if err != nil {
		return nil
	}
	info := sd.github.GetInfo(ctx, sd.gitcmd)
	if git.BranchNameRegex.FindString(info.LocalBranch) != "" {
		fmt.Printf("error: don't run spr in a remote pr branch\n")
		fmt.Printf(" this could lead to weird duplicate pull requests getting created\n")
		fmt.Printf(" in general there is no need to checkout remote branches used for prs\n")
		fmt.Printf(" instead use local branches and run spr update to sync your commit stack\n")
		fmt.Printf("  with your pull requests on github\n")
		fmt.Printf("branch name: %s\n", info.LocalBranch)
		return nil
	}

	return info
}

// syncCommitStackToGitHub gets all the local commits in the given branch
//
//	which are new (on top of remote branch) and creates a corresponding
//	branch on github for each commit.
func (sd *stackediff) syncCommitStackToGitHub(ctx context.Context,
	commits []git.Commit, info *github.GitHubInfo) bool {

	var output string
	sd.gitcmd.MustGit("status --porcelain --untracked-files=no", &output)
	if output != "" {
		err := sd.gitcmd.Git("stash", nil)
		if err != nil {
			return false
		}
		defer sd.gitcmd.MustGit("stash pop", nil)
	}

	commitUpdated := func(c git.Commit, info *github.GitHubInfo) bool {
		for _, pr := range info.PullRequests {
			if pr.Commit.CommitID == c.CommitID {
				return pr.Commit.CommitHash != c.CommitHash
			}
		}
		return true
	}

	var updatedCommits []git.Commit
	for _, commit := range commits {
		if commit.WIP {
			break
		}
		if commitUpdated(commit, info) {
			updatedCommits = append(updatedCommits, commit)
		}
	}

	var refNames []string
	for _, commit := range updatedCommits {
		branchName := git.BranchNameFromCommit(sd.config, commit)
		refNames = append(refNames,
			commit.CommitHash+":refs/heads/"+branchName)
	}

	if len(updatedCommits) > 0 {
		if sd.config.Repo.BranchPushIndividually {
			for _, refName := range refNames {
				pushCommand := fmt.Sprintf("push --force %s %s", sd.config.Repo.GitHubRemote, refName)
				sd.gitcmd.MustGit(pushCommand, nil)
			}
		} else {
			pushCommand := fmt.Sprintf("push --force --atomic %s ", sd.config.Repo.GitHubRemote)
			pushCommand += strings.Join(refNames, " ")
			sd.gitcmd.MustGit(pushCommand, nil)
		}
	}
	sd.profiletimer.Step("SyncCommitStack::PushBranches")
	return true
}

func check(err error) {
	if err != nil {
		if os.Getenv("SPR_DEBUG") == "1" {
			panic(err)
		}
		fmt.Printf("error: %s\n", err)
		os.Exit(1)
	}
}

func header(config *config.Config) string {
	if config.User.PRSetWorkflows {
		if config.User.StatusBitsEmojis {
			return `
 ┌─ commit index
 │ ┌─ pull request set index
 │ │   ┌─ github checks pass
 │ │   │ ┌── pull request approved
 │ │   │ │ ┌─── no merge conflicts
 │ │   │ │ │ ┌──── stack check
 │ │   │ │ │ │
`
		} else {
			return `
 ┌─ commit index
 │ ┌─ pull request set index
 │ │   ┌─ github checks pass
 │ │   │┌── pull request approved
 │ │   ││┌─── no merge conflicts
 │ │   │││┌──── stack check
 │ │   ││││
`
		}
	} else {
		if config.User.StatusBitsEmojis {
			return `
 ┌─ github checks pass
 │ ┌── pull request approved
 │ │ ┌─── no merge conflicts
 │ │ │ ┌──── stack check
 │ │ │ │
`
		} else {
			return `
 ┌─ github checks pass
 │┌── pull request approved
 ││┌─── no merge conflicts
 │││┌──── stack check
 ││││
`
		}
	}
}
