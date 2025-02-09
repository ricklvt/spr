package gitapi

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/git/realgit"
	ngit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	gogithub "github.com/google/go-github/v69/github"
)

type GitApi struct {
	config     *config.Config
	repo       *ngit.Repository
	goghclient *gogithub.Client
}

func New(config *config.Config, repo *ngit.Repository, goghclient *gogithub.Client) GitApi {
	return GitApi{config: config, repo: repo, goghclient: goghclient}
}

// OriginMainRef returns the ref for the default remote and the default branch (often origin/main)
func (gapi GitApi) OriginMainRef(ctx context.Context) (*plumbing.Reference, error) {
	branch := gapi.config.Repo.GitHubBranch
	remote := gapi.config.Repo.GitHubRemote

	originMainRef, err := gapi.repo.Reference(plumbing.ReferenceName(fmt.Sprintf("refs/remotes/%s/%s", remote, branch)), true)
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s HEAD %w", remote, branch, err)
	}

	return originMainRef, nil
}

func (gapi GitApi) AppendCommitId() error {
	// The "github.com/go-git/go-git/" doesn't (easily) support updating a commit message so we have to do this by
	// shelling out to the command line
	gitshell := realgit.NewGitCmd(gapi.config)

	rewordPath, err := exec.LookPath("spr_reword_helper")
	if err != nil {
		fmt.Errorf("can't find spr_reword_helper %w", err)
	}
	rebaseCommand := fmt.Sprintf(
		"rebase %s/%s -i --autosquash --autostash",
		gapi.config.Repo.GitHubRemote,
		gapi.config.Repo.GitHubBranch,
	)
	err = gitshell.GitWithEditor(rebaseCommand, nil, rewordPath)
	if err != nil {
		fmt.Errorf("can't execute spr_reword_helper %w", err)
	}

	return nil
}
