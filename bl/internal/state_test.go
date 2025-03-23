package internal_test

import (
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ejoffe/spr/bl/internal"
	bl "github.com/ejoffe/spr/bl/internal"
	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/git"
	"github.com/ejoffe/spr/github"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithub "github.com/google/go-github/v69/github"
	"github.com/stretchr/testify/require"
)

func TestAssignPullRequests(t *testing.T) {
	config := config.EmptyConfig()
	config.Repo.GitHubRepoName = t.Name()
	config.State.RepoToCommitIdToPRSet[t.Name()] = map[string]int{
		"11111111": 1,
		"22222222": 0,
		"99999999": 9,
	}
	gitCommits := []*internal.PRCommit{
		{
			Commit: git.Commit{
				CommitHash: "H1111111",
				CommitID:   "11111111",
			},
		},
		{
			Commit: git.Commit{
				CommitHash: "H2222222",
				CommitID:   "22222222",
			},
		},
		{
			Commit: git.Commit{
				CommitHash: "H3333333",
				CommitID:   "33333333",
			},
		},
	}

	prMap := map[string]*github.PullRequest{
		"11111111": {
			ID: "1",
		},
		"22222222": {
			ID: "2",
		},
		"99999999": {
			ID: "9",
		},
	}

	expectedOrphanedPRs := mapset.NewSet[*github.PullRequest]()
	expectedOrphanedPRs.Add(prMap["99999999"])

	orphanedPRs := internal.AssignPullRequests(config, gitCommits, prMap)

	// The PR is set
	require.Equal(t, "1", gitCommits[0].PullRequest.ID)
	require.Equal(t, "2", gitCommits[1].PullRequest.ID)
	require.Nil(t, gitCommits[2].PullRequest)

	// The PRIndex is set
	require.Equal(t, 1, *gitCommits[0].PRIndex)
	require.Equal(t, 0, *gitCommits[1].PRIndex)
	require.Nil(t, gitCommits[2].PRIndex)

	// The PR also references the commit
	require.Equal(t, gitCommits[0].CommitHash, gitCommits[0].PullRequest.Commit.CommitHash)
	require.Equal(t, gitCommits[1].CommitHash, gitCommits[1].PullRequest.Commit.CommitHash)

	// The extra PR is returned as orphaned
	require.Equal(t, expectedOrphanedPRs, orphanedPRs)

	// Since 99999999 isn't used it should be removed from the mapping
	_, ok := config.State.RepoToCommitIdToPRSet[t.Name()]["99999999"]
	require.False(t, ok)

}

func TestSetStackedCheck(t *testing.T) {
	config := &config.Config{
		Repo: &config.RepoConfig{
			RequireChecks:   true,
			RequireApproval: true,
		},
	}
	pass := func() *bl.PRCommit {
		return &bl.PRCommit{
			Commit: git.Commit{
				WIP: false,
			},
			PullRequest: &github.PullRequest{
				MergeStatus: github.PullRequestMergeStatus{
					ChecksPass:     github.CheckStatusPass,
					ReviewApproved: true,
					NoConflicts:    true,
				},
			},
		}
	}
	fail := pass()
	fail.Commit.WIP = true
	commits := []*bl.PRCommit{
		pass(),
		fail,
		pass(),
	}

	bl.SetStackedCheck(config, commits)
	require.True(t, commits[2].PullRequest.MergeStatus.Stacked)
	require.False(t, commits[1].PullRequest.MergeStatus.Stacked)
	require.False(t, commits[0].PullRequest.MergeStatus.Stacked)
}

func TestApplyIndicies(t *testing.T) {
	// Define the PRs here so the pointer value will be consistent between calls of testingState
	// this allow us to compare sets containing &github.PullRequest
	pr0 := &github.PullRequest{ID: "0"}
	pr1 := &github.PullRequest{ID: "1"}
	pr2 := &github.PullRequest{ID: "2"}
	pr3 := &github.PullRequest{ID: "3"}
	testingState := func() *internal.State {
		gitCommits := []*internal.PRCommit{
			{
				Index:       0,
				PRIndex:     gogithub.Ptr(0),
				PullRequest: pr0,
			},
			{
				Index:       1,
				PRIndex:     gogithub.Ptr(0),
				PullRequest: pr1,
			},
			{
				Index:       2,
				PRIndex:     gogithub.Ptr(1),
				PullRequest: pr2,
			},
			{
				Index:       3,
				PRIndex:     gogithub.Ptr(2),
				PullRequest: pr3,
			},
			{
				Index: 4,
			},
		}
		return &internal.State{
			Commits:       gitCommits,
			OrphanedPRs:   mapset.NewSet[*github.PullRequest](),
			MutatedPRSets: mapset.NewSet[int](),
		}
	}

	tests := []struct {
		desc                       string
		destinationPRIndex         *int
		commitIndex                mapset.Set[int]
		expectedState              func() *internal.State
		expectedDestinationPRINdex *int
	}{
		{
			desc:               "apply to un-PRs commit",
			destinationPRIndex: nil,
			commitIndex:        mapset.NewSet[int](4),
			expectedState: func() *internal.State {
				state := testingState()
				state.Commits[4].PRIndex = gogithub.Ptr(3)
				state.MutatedPRSets = mapset.NewSet[int](3)
				return state
			},
			expectedDestinationPRINdex: gogithub.Ptr(3),
		}, {
			desc:               "no-op - update with same PR",
			destinationPRIndex: gogithub.Ptr(1),
			commitIndex:        mapset.NewSet[int](2),
			expectedState: func() *internal.State {
				state := testingState()
				return state
			},
			expectedDestinationPRINdex: gogithub.Ptr(1),
		}, {
			desc:               "no-op, - no commits are part of a new PR set",
			destinationPRIndex: nil,
			commitIndex:        mapset.NewSet[int](),
			expectedState: func() *internal.State {
				state := testingState()
				return state
			},
			expectedDestinationPRINdex: nil,
		}, {
			desc:               "merge two PR sets, only needs to mutate the updated set (not the deleted set)",
			destinationPRIndex: gogithub.Ptr(1),
			commitIndex:        mapset.NewSet[int](2, 3),
			expectedState: func() *internal.State {
				state := testingState()
				state.Commits[3].PRIndex = gogithub.Ptr(1)
				state.MutatedPRSets = mapset.NewSet[int](1) // Don't need to mutate PRSet 2 as it's been replaced
				return state
			},
			expectedDestinationPRINdex: gogithub.Ptr(1),
		}, {
			desc:               "split a PR set needs both PR sets updated",
			destinationPRIndex: nil,
			commitIndex:        mapset.NewSet[int](0),
			expectedState: func() *internal.State {
				state := testingState()
				state.Commits[0].PRIndex = gogithub.Ptr(3)
				state.MutatedPRSets = mapset.NewSet[int](0, 3)
				return state
			},
			expectedDestinationPRINdex: gogithub.Ptr(3),
		}, {
			desc:               "deleting a PR set adds the existing PRs to the OrphanedPRs",
			destinationPRIndex: gogithub.Ptr(0),
			commitIndex:        mapset.NewSet[int](),
			expectedState: func() *internal.State {
				state := testingState()
				state.Commits[0].PRIndex = nil
				state.Commits[1].PRIndex = nil
				state.OrphanedPRs.Add(state.Commits[0].PullRequest)
				state.OrphanedPRs.Add(state.Commits[1].PullRequest)
				return state
			},
			expectedDestinationPRINdex: gogithub.Ptr(0),
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			state := testingState()
			indices := internal.Indices{DestinationPRIndex: test.destinationPRIndex, CommitIndexes: test.commitIndex}
			state.ApplyIndices(&indices)

			require.Equal(t, test.expectedState(), state)
			if test.expectedDestinationPRINdex == nil {
				require.Nil(t, indices.DestinationPRIndex)
			} else {
				require.Equal(t, *test.expectedDestinationPRINdex, *indices.DestinationPRIndex)
			}
		})
	}
}

func TestCommitsByPRSet(t *testing.T) {
	// Define the PRs here so the pointer value will be consistent between calls of testingState
	// this allow us to compare sets containing &github.PullRequest
	pr0 := &github.PullRequest{ID: "0"}
	pr1 := &github.PullRequest{ID: "1"}
	pr2 := &github.PullRequest{ID: "2"}
	testingState := internal.State{
		Commits: []*internal.PRCommit{
			{
				Index:       0,
				PRIndex:     gogithub.Ptr(0),
				PullRequest: pr0,
			},
			{
				Index:       1,
				PRIndex:     gogithub.Ptr(1),
				PullRequest: pr1,
			},
			{
				Index:       2,
				PRIndex:     gogithub.Ptr(2),
				PullRequest: pr2,
			},
			{
				Index:       3,
				PRIndex:     gogithub.Ptr(0),
				PullRequest: pr0,
			},
			{
				Index: 4,
			},
		},
	}

	commitsByPRSet := testingState.CommitsByPRSet(0)
	require.Len(t, commitsByPRSet, 2)
	require.Equal(t, 0, commitsByPRSet[0].Index)
	require.Equal(t, 3, commitsByPRSet[1].Index)
}

func TestMutatedPRSetsWithOutOfOrderCommits(t *testing.T) {
	// A PR set which is in order is one where the Nth To branch matches the N+1 From branch
	testingState := internal.State{
		Commits: []*internal.PRCommit{
			// Start PR set 1
			{
				PullRequest: &github.PullRequest{
					ToBranch: "0",
				},
				PRIndex: gogithub.Ptr(0),
			},
			{
				PullRequest: &github.PullRequest{
					FromBranch: "0",
					ToBranch:   "1",
				},
				PRIndex: gogithub.Ptr(0),
			},
			{
				// Just a commit without a PR
			},
			// Start PR set 1 which is out of order
			{
				PullRequest: &github.PullRequest{
					ToBranch: "0",
				},
				PRIndex: gogithub.Ptr(1),
			},
			{
				PullRequest: &github.PullRequest{
					FromBranch: "1",
				},
				PRIndex: gogithub.Ptr(1),
			},
			// Resume PR set 0
			{
				PullRequest: &github.PullRequest{
					FromBranch: "1",
					ToBranch:   "2",
				},
				PRIndex: gogithub.Ptr(0),
			},
		},
	}

	testingState.MutatedPRSets = mapset.NewSet[int](0)
	require.True(t, testingState.MutatedPRSetsWithOutOfOrderCommits().IsEmpty())
	testingState.MutatedPRSets = mapset.NewSet[int](1)
	require.True(t, testingState.MutatedPRSetsWithOutOfOrderCommits().Contains(1))
}

func TestPullRequests(t *testing.T) {
	pr0 := &github.PullRequest{ID: "0"}
	pr1 := &github.PullRequest{ID: "1"}
	pr2 := &github.PullRequest{ID: "2"}
	pr3 := &github.PullRequest{ID: "3"}
	testingCommits := []*internal.PRCommit{
		{
			Index:       0,
			PRIndex:     gogithub.Ptr(0),
			PullRequest: pr0,
		},
		{
			Index:       1,
			PRIndex:     gogithub.Ptr(0),
			PullRequest: pr1,
		},
		{
			Index:       2,
			PRIndex:     gogithub.Ptr(1),
			PullRequest: pr2,
		},
		{
			Index:       3,
			PRIndex:     gogithub.Ptr(2),
			PullRequest: pr3,
		},
		{
			Index: 4,
		},
	}

	expectedPullRequests := []*github.PullRequest{
		pr0, pr1, pr2, pr3,
	}
	pullRequests := internal.PullRequests(testingCommits)
	require.Equal(t, expectedPullRequests, pullRequests)
}

func TestGeneratePullRequestMap(t *testing.T) {
	t.Run("handles no PRs", func(t *testing.T) {
		prMap := bl.GeneratePullRequestMap([]bl.PullRequestStatus{})
		require.Equal(t, map[string]*github.PullRequest{}, prMap)
	})

	t.Run("computes key based on head branch", func(t *testing.T) {
		prMap := bl.GeneratePullRequestMap([]bl.PullRequestStatus{
			{
				PullRequest: &gogithub.PullRequest{
					ID: gogithub.Ptr(int64(3)),
					Head: &gogithub.PullRequestBranch{
						Ref: gogithub.Ptr("spr/main/0f47588b"),
					},
				},
			},
		})
		expected := map[string]*github.PullRequest{
			"0f47588b": &github.PullRequest{
				ID:         "3",
				FromBranch: "spr/main/0f47588b",
			},
		}
		require.Equal(t, expected, prMap)
	})
}

func TestCommitIdFromBranch(t *testing.T) {
	require.Equal(t, "", bl.CommitIdFromBranch(""))
	require.Equal(t, "", bl.CommitIdFromBranch("spr/"))
	require.Equal(t, "", bl.CommitIdFromBranch("spr/main"))
	require.Equal(t, "", bl.CommitIdFromBranch("spr/main/1234444"))
	require.Equal(t, "", bl.CommitIdFromBranch("other/main/12344448"))
	require.Equal(t, "12344448", bl.CommitIdFromBranch("spr/main/12344448"))
}

func TestComputeMergeStatus(t *testing.T) {
	tests := []struct {
		name string
		prs  bl.PullRequestStatus
		prms github.PullRequestMergeStatus
	}{{
		name: "no status checks",
		prs: bl.PullRequestStatus{
			PullRequest: &gogithub.PullRequest{},
			CombinedStatus: &gogithub.CombinedStatus{
				State:      gogithub.Ptr("pending"),
				TotalCount: gogithub.Ptr(0),
			},
			Reviews: []*gogithub.PullRequestReview{},
		},
		prms: github.PullRequestMergeStatus{
			ChecksPass:     github.CheckStatusPass,
			NoConflicts:    false,
			ReviewApproved: false,
		},
	},
		{
			name: "nil values",
			prs: bl.PullRequestStatus{
				PullRequest:    &gogithub.PullRequest{},
				CombinedStatus: &gogithub.CombinedStatus{TotalCount: gogithub.Ptr(1)},
				Reviews:        []*gogithub.PullRequestReview{},
			},
			prms: github.PullRequestMergeStatus{
				ChecksPass:     github.CheckStatusUnknown,
				NoConflicts:    false,
				ReviewApproved: false,
			},
		}, {
			name: "nil(er) values",
			prs: bl.PullRequestStatus{
				PullRequest:    nil,
				CombinedStatus: nil,
				Reviews:        nil,
			},
			prms: github.PullRequestMergeStatus{
				ChecksPass:     github.CheckStatusUnknown,
				NoConflicts:    false,
				ReviewApproved: false,
			},
		}, {
			name: "checks pass, approved",
			prs: bl.PullRequestStatus{
				PullRequest:    &gogithub.PullRequest{},
				CombinedStatus: &gogithub.CombinedStatus{State: gogithub.Ptr("success"), TotalCount: gogithub.Ptr(1)},
				Reviews: []*gogithub.PullRequestReview{
					{
						State: gogithub.Ptr("APPROVED"),
					},
				},
			},
			prms: github.PullRequestMergeStatus{
				ChecksPass:     github.CheckStatusPass,
				NoConflicts:    false,
				ReviewApproved: true,
			},
		}, {
			name: "checks pending, no conflicts",
			prs: bl.PullRequestStatus{
				PullRequest: &gogithub.PullRequest{
					Mergeable: gogithub.Bool(true),
				},
				CombinedStatus: &gogithub.CombinedStatus{State: gogithub.Ptr("pending"), TotalCount: gogithub.Ptr(1)},
				Reviews:        []*gogithub.PullRequestReview{},
			},
			prms: github.PullRequestMergeStatus{
				ChecksPass:     github.CheckStatusPending,
				NoConflicts:    true,
				ReviewApproved: false,
			},
		}, {
			name: "checks fail",
			prs: bl.PullRequestStatus{
				PullRequest:    &gogithub.PullRequest{},
				CombinedStatus: &gogithub.CombinedStatus{State: gogithub.Ptr("failure"), TotalCount: gogithub.Ptr(1)},
				Reviews:        []*gogithub.PullRequestReview{},
			},
			prms: github.PullRequestMergeStatus{
				ChecksPass:     github.CheckStatusFail,
				NoConflicts:    false,
				ReviewApproved: false,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.prms, bl.ComputeMergeStatus(test.prs))
		})
	}
}

func TestGenerateCommits_LinksCommitsAndSetsIndicies(t *testing.T) {
	commits := bl.GenerateCommits(
		[]*object.Commit{
			{
				Hash:    plumbing.NewHash("01"),
				Message: "commit-id:11111111",
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("02"),
				},
			},
			{
				Hash:    plumbing.NewHash("02"),
				Message: "commit-id:22222222",
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("03"),
				},
			},
			{
				Hash:    plumbing.NewHash("03"),
				Message: "commit-id:33333333",
			},
		},
	)

	require.Equal(t, 2, commits[0].Index)
	require.Equal(t, 1, commits[0].Parent.Index)
	require.Equal(t, 0, commits[0].Parent.Parent.Index)

	require.Equal(t, "11111111", commits[0].CommitID)
	require.Equal(t, "22222222", commits[0].Parent.CommitID)
	require.Equal(t, "33333333", commits[0].Parent.Parent.CommitID)
	require.Equal(t, "11111111", commits[0].Parent.Parent.Child.Child.CommitID)
}

func TestUpdatePRSetState(t *testing.T) {
	config := config.EmptyConfig()
	config.Repo.GitHubRepoName = t.Name()
	config.State.RepoToCommitIdToPRSet["other"] = map[string]int{
		"44444444": 4,
	}
	config.State.RepoToCommitIdToPRSet[t.Name()] = map[string]int{
		"11111111": 1,
		"22222222": 0,
		"99999999": 9,
	}

	testingCommits := []*internal.PRCommit{
		{
			Commit: git.Commit{
				CommitID: "11111111",
			},
			PRIndex: gogithub.Ptr(0),
		},
		{
			Commit: git.Commit{
				CommitID: "22222222",
			},
			PRIndex: gogithub.Ptr(0),
		},
		{
			Commit: git.Commit{
				CommitID: "33333333",
			},
			PRIndex: gogithub.Ptr(1),
		},
		{
			Commit: git.Commit{
				CommitID: "44444444",
			},
			PRIndex: gogithub.Ptr(2),
		},
		{
			Commit: git.Commit{
				CommitID: "55555555",
			},
		},
	}

	expectedStateMap := map[string]map[string]int{
		"other": map[string]int{
			"44444444": 4,
		},
		t.Name(): map[string]int{
			"11111111": 0,
			"22222222": 0,
			"33333333": 1,
			"44444444": 2,
		},
	}

	state := internal.State{Commits: testingCommits}

	state.UpdatePRSetState(config)

	require.Equal(t, expectedStateMap, config.State.RepoToCommitIdToPRSet)

}

func TestHeadFirst(t *testing.T) {
	t.Run("preserves HEAD first", func(t *testing.T) {
		res := bl.HeadFirst([]*object.Commit{
			{
				Hash:    plumbing.NewHash("01"),
				Message: "HEAD",
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("03"),
					plumbing.NewHash("02"),
				},
			},
			{
				Hash: plumbing.NewHash("02"),
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("04"),
					plumbing.NewHash("05"),
				},
			},
		})
		require.Equal(t, "HEAD", res[0].Message)
	})

	t.Run("sorts HEAD first", func(t *testing.T) {
		res := bl.HeadFirst([]*object.Commit{
			{
				Hash: plumbing.NewHash("02"),
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("04"),
					plumbing.NewHash("05"),
				},
			},
			{
				Hash:    plumbing.NewHash("01"),
				Message: "HEAD",
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("03"),
					plumbing.NewHash("02"),
				},
			},
		})
		require.Equal(t, "HEAD", res[0].Message)
	})
}

func TestCommitId(t *testing.T) {
	require.Equal(t, "c0530239", bl.CommitId("msg\nsdf\ncommit-id:c0530239"))
	require.Equal(t, "c0530239", bl.CommitId("msg\nsdf\ncommit-id:c0530239\nasdf"))
	require.Equal(t, "c0530239", bl.CommitId("commit-id:c0530239"))
	require.Equal(t, "", bl.CommitId("commit-id:c053023999")) // extra character
	require.Equal(t, "", bl.CommitId("xcommit-id:c0530239"))
	require.Equal(t, "", bl.CommitId(""))
	require.Equal(t, "", bl.CommitId("\n\ncommit-id:"))
}

func TestIsWIP(t *testing.T) {
	require.True(t, bl.IsWIP("WIP\nsother text"))
	require.False(t, bl.IsWIP("nop\nsother text"))
}

func TestSubject(t *testing.T) {
	require.Equal(t, "msg", bl.Subject("msg\nsdf\nsdf"))
	require.Equal(t, "msg", bl.Subject("msg\nsdf"))
	require.Equal(t, "msg", bl.Subject("msg\n"))
	require.Equal(t, "msg", bl.Subject("msg"))
	require.Equal(t, "", bl.Subject("\nmsg"))
	require.Equal(t, "", bl.Subject(""))
}

func TestBody(t *testing.T) {
	require.Equal(t, "sdf\nsdf", bl.Body("msg\nsdf\nsdf"))
	require.Equal(t, "sdf", bl.Body("msg\nsdf"))
	require.Equal(t, "", bl.Body("msg\n"))
	require.Equal(t, "", bl.Body("msg"))
	require.Equal(t, "msg", bl.Body("\nmsg"))
	require.Equal(t, "", bl.Body(""))
}
