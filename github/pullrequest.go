package github

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ejoffe/spr/config"
	"github.com/ejoffe/spr/git"
	"github.com/ejoffe/spr/terminal"
)

// PullRequest has GitHub pull request data
type PullRequest struct {
	ID         string
	Number     int
	FromBranch string
	ToBranch   string
	Commit     git.Commit
	Title      string
	Body       string

	MergeStatus PullRequestMergeStatus
	Merged      bool
	Commits     []git.Commit
	InQueue     bool
}

type checkStatus int

const (
	// CheckStatusUnknown
	CheckStatusUnknown checkStatus = iota

	// CheckStatusPending when checks are still running
	CheckStatusPending

	// CheckStatusPass when all checks pass
	CheckStatusPass

	// CheckStatusFail when some chechs have failed
	CheckStatusFail
)

// PullRequestMergeStatus is the merge status of a pull request
type PullRequestMergeStatus struct {
	// ChecksPass is the status of GitHub checks
	ChecksPass checkStatus

	// ReviewApproved is true when a pull request is approved by a fellow reviewer
	ReviewApproved bool

	// NoConflicts is true when there are no merge conflicts
	NoConflicts bool

	// Stacked is true when all requests in the stack up to this one are ready to merge
	Stacked bool
}

// Mergeable returns true if the pull request is mergable
func (pr *PullRequest) Mergeable(config *config.Config) bool {
	if !pr.MergeStatus.NoConflicts {
		return false
	}
	if !pr.MergeStatus.Stacked {
		return false
	}
	if config.Repo.RequireChecks && pr.MergeStatus.ChecksPass != CheckStatusPass {
		return false
	}
	if config.Repo.RequireApproval && !pr.MergeStatus.ReviewApproved {
		return false
	}
	return true
}

// Ready returns true if pull request is ready to merge
func (pr *PullRequest) Ready(config *config.Config) bool {
	if pr.Commit.WIP {
		return false
	}
	if !pr.MergeStatus.NoConflicts {
		return false
	}
	if config.Repo.RequireChecks && pr.MergeStatus.ChecksPass != CheckStatusPass {
		return false
	}
	if config.Repo.RequireApproval && !pr.MergeStatus.ReviewApproved {
		return false
	}
	return true
}

const (
	// Terminal escape codes for colors
	ColorReset     = "\033[0m"
	ColorRed       = "\033[31m"
	ColorGreen     = "\033[32m"
	ColorBlue      = "\033[34m"
	ColorLightBlue = "\033[1;34m"

	// ascii status bits
	asciiCheckmark = "v"
	asciiCrossmark = "x"
	asciiPending   = "."
	asciiQuerymark = "?"
	asciiEmpty     = "-"
	asciiWarning   = "!"

	// emoji status bits
	emojiCheckmark    = "✅"
	emojiCrossmark    = "❌"
	emojiPending      = "⌛"
	emojiQuestionmark = "❓"
	emojiEmpty        = "➖"
	emojiWarning      = "⚠️"
)

func StatusBitIcons(config *config.Config) map[string]string {
	if config.User.StatusBitsEmojis {
		return map[string]string{
			"checkmark":    emojiCheckmark,
			"crossmark":    emojiCrossmark,
			"pending":      emojiPending,
			"questionmark": emojiQuestionmark,
			"empty":        emojiEmpty,
			"warning":      emojiWarning,
		}
	} else {
		return map[string]string{
			"checkmark":    asciiCheckmark,
			"crossmark":    asciiCrossmark,
			"pending":      asciiPending,
			"questionmark": asciiQuerymark,
			"empty":        asciiEmpty,
			"warning":      asciiWarning,
		}
	}
}

// StatusString returs a string representation of the merge status bits
func (pr *PullRequest) StatusString(config *config.Config) string {
	icons := StatusBitIcons(config)
	statusString := "["

	statusString += pr.MergeStatus.ChecksPass.String(config)

	if config.Repo.RequireApproval {
		if pr.MergeStatus.ReviewApproved {
			statusString += icons["checkmark"]
		} else {
			statusString += icons["crossmark"]
		}
	} else {
		statusString += icons["empty"]
	}

	if pr.MergeStatus.NoConflicts {
		statusString += icons["checkmark"]
	} else {
		statusString += icons["crossmark"]
	}

	if pr.MergeStatus.Stacked {
		statusString += icons["checkmark"]
	} else {
		statusString += icons["crossmark"]
	}

	statusString += "]"
	return statusString
}

func padNumber(pad int) func(string) string {
	return func(s string) string {
		padding := pad - len(s)
		if padding > 0 {
			s += strings.Repeat(" ", padding)
		}
		return s
	}
}

func (pr *PullRequest) String(config *config.Config) string {
	prStatus := pr.StatusString(config)
	if pr.Merged {
		prStatus = "MERGED"
	}

	padding := func(s string) string { return s }
	if config.User.PRSetWorkflows {
		padding = padNumber(5)
	}

	prInfo := padding(fmt.Sprintf("%3d", pr.Number))
	if config.User.ShowPRLink {
		prInfo = fmt.Sprintf("https://%s/%s/%s/pull/%s",
			config.Repo.GitHubHost, config.Repo.GitHubRepoOwner, config.Repo.GitHubRepoName, padding(fmt.Sprintf("%d", pr.Number)))
	}

	var mq string
	if len(pr.Commits) > 1 {
		mq = StatusBitIcons(config)["warning"]
	}

	if pr.InQueue {
		mq = StatusBitIcons(config)["pending"]
	}

	if mq != "" {
		mq += " "
	}

	line := fmt.Sprintf("%s %s%s : %s", prStatus, mq, prInfo, pr.Title)

	return TrimToTerminal(config, line)
}

func TrimToTerminal(config *config.Config, line string) string {
	// trim line to terminal width
	terminalWidth, err := terminal.Width()
	if err != nil {
		terminalWidth = 1000
	}
	lineLength := utf8.RuneCountInString(line)
	if config.User.StatusBitsEmojis {
		// each emoji consumes 2 chars in the terminal
		lineLength += 4
	}
	diff := lineLength - terminalWidth
	if diff > 0 && terminalWidth > 3 {
		line = line[:terminalWidth-3] + "..."
	}

	return line
}

func (cs checkStatus) String(config *config.Config) string {
	icons := StatusBitIcons(config)
	if config.Repo.RequireChecks {
		switch cs {
		case CheckStatusUnknown:
			return icons["questionmark"]
		case CheckStatusPending:
			return icons["pending"]
		case CheckStatusFail:
			return icons["crossmark"]
		case CheckStatusPass:
			return icons["checkmark"]
		default:
			return icons["questionmark"]
		}
	}
	return icons["empty"]
}
