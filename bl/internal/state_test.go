package internal_test

import (
	"testing"

	bl "github.com/ejoffe/spr/bl/internal"
	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/git"
	"github.com/ejoffe/spr/github"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithub "github.com/google/go-github/v69/github"
	"github.com/stretchr/testify/require"
)

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

func TestGenerateCommits_LinksCommits(t *testing.T) {
	commits := bl.GenerateCommits(
		[]*object.Commit{
			{
				Hash:    plumbing.NewHash("01"),
				Message: "1",
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("02"),
				},
			},
			{
				Hash:    plumbing.NewHash("02"),
				Message: "2",
				ParentHashes: []plumbing.Hash{
					plumbing.NewHash("03"),
				},
			},
			{
				Hash:    plumbing.NewHash("03"),
				Message: "3",
			},
		},
	)

	require.Equal(t, 2, commits[0].Index)
	require.Equal(t, 1, commits[0].Parent.Index)
	require.Equal(t, 0, commits[0].Parent.Parent.Index)

	require.Equal(t, "1", commits[0].Subject)
	require.Equal(t, "2", commits[0].Parent.Subject)
	require.Equal(t, "3", commits[0].Parent.Parent.Subject)
	require.Equal(t, "1", commits[0].Parent.Parent.Child.Child.Subject)
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
