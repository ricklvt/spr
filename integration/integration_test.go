//go:build integration

package integration

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/ejoffe/spr/bl"
	"github.com/ejoffe/spr/bl/gitapi"
	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/config/config_parser"
	"github.com/ejoffe/spr/git/realgit"
	"github.com/ejoffe/spr/github"
	"github.com/ejoffe/spr/github/githubclient"
	"github.com/ejoffe/spr/spr"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithub "github.com/google/go-github/v69/github"
	"github.com/stretchr/testify/require"
)

// SacrificialRepo is an env var that must contain the path to the sacrificial repo for the integration tests to be
// executed.
const SacrificialRepo = "SACRIFICIAL_REPO"

// SacrificialRepo is a file that must exist in the repo or the tests won't run. This is an additional protection
// against running the tests against a "real" repo.
const SacrificialFile = "spr.sacrificial"

// prefix is a unique string that will be used to make git files and commit messages unique
var prefix string = ""

// resoruces contains various resources for unit testing
type resources struct {
	repo      *git.Repository
	stackedpr *spr.Stackediff
	sb        *strings.Builder
	cleanup   func()
}

func initialize(t *testing.T, cfgfn func(*config.Config)) *resources {
	t.Helper()

	// Make sure we are working with a sacrificial repoPath
	repoPath := os.Getenv(SacrificialRepo)
	require.NotEqual(t, "", repoPath, fmt.Sprintf("must set the %s env var", SacrificialRepo))

	if !fileExists(path.Join(repoPath, ".git/config")) {
		require.Failf(t, "\"%s\" is not a git repo", SacrificialRepo)
	}
	if !fileExists(path.Join(repoPath, SacrificialFile)) {
		require.Failf(t, "\"%s\" is not marked as a sacrificial repo. Add and commit a file named \"%s\" to allow these integration tests to use it. Note this should not be done with any repo that contains valuable data.", SacrificialRepo, SacrificialFile)
	}
	err := os.Chdir(repoPath)
	require.NoError(t, err)

	// Create a unique identifier for this execution
	prefix = fmt.Sprintf("%d-", rand.Int())

	// Parse the config then overwrite the state and the global settings
	// This is so we can re-use the repos settings.
	gitcmd := realgit.NewGitCmd(config.DefaultConfig())
	//  check that we are inside a git dir
	var output string
	err = gitcmd.Git("status --porcelain", &output)
	require.NoError(t, err)

	cfg := config_parser.ParseConfig(gitcmd)
	// Overwrite State and User so the test has a consistent experience.
	cfgdefault := config.DefaultConfig()
	cfg.State = cfgdefault.State
	cfg.User = cfgdefault.User
	cfg.State.Stargazer = true
	cfgfn(cfg)

	err = config_parser.CheckConfig(cfg)
	require.NoError(t, err)

	gitcmd = realgit.NewGitCmd(cfg)
	wd, err := os.Getwd()
	require.NoError(t, err)

	repo, err := git.PlainOpen(wd)
	require.NoError(t, err)

	goghclient := gogithub.NewClient(nil).WithAuthToken(github.FindToken(cfg.Repo.GitHubHost))

	ctx := context.Background()
	client := githubclient.NewGitHubClient(ctx, cfg)
	stackedpr := spr.NewStackedPR(cfg, client, gitcmd, repo, goghclient)

	// Direct the output to a strings.Builder so we can test against the output
	var sb strings.Builder
	stackedpr.Output = &sb

	// Create a cleanup function to try and reset the repo
	cleanupFn := func() {
		state, err := bl.NewReadState(ctx, cfg, goghclient, repo)
		require.NoError(t, err)

		gitapi := gitapi.New(cfg, repo, goghclient)
		for _, commit := range state.Commits {
			if commit.PullRequest != nil {
				gitapi.DeletePullRequest(ctx, commit.PullRequest)
			}
		}

		err = gitcmd.Git(fmt.Sprintf("reset --hard %s/%s", cfg.Repo.GitHubRemote, cfg.Repo.GitHubBranch), &output)
		require.NoError(t, err)
	}

	// Run the cleanup now
	cleanupFn()

	return &resources{
		repo:      repo,
		stackedpr: stackedpr,
		sb:        &sb,
		cleanup:   cleanupFn,
	}
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

// commit is the contents to add to a commit. If the filename exists the contents will be appended.
type commit struct {
	filename string
	contents string
}

// createCommits creates the commits
func createCommits(t *testing.T, repo *git.Repository, commits []commit) {
	t.Helper()

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	for _, commit := range commits {
		func() {
			file, err := os.OpenFile(commit.filename, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
			require.NoError(t, err)
			defer file.Close()

			_, err = file.WriteString(commit.contents)
			require.NoError(t, err)
		}()
		_, err = worktree.Add(commit.filename)
		require.NoError(t, err)

		commit, err := worktree.Commit(commit.contents, &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Testy McTestFace",
				Email: "testy.mctestface@example.com",
				When:  time.Now(),
			},
		})
		require.NoError(t, err)

		_, err = repo.CommitObject(commit)
		require.NoError(t, err)
	}
}
