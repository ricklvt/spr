package github

import (
	"fmt"
	"os"
	"path"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

// hub cli config (https://hub.github.com)
type hubCLIConfig map[string][]struct {
	User       string `yaml:"user"`
	OauthToken string `yaml:"oauth_token"`
	Protocol   string `yaml:"protocol"`
}

// gh cli config (https://cli.github.com)
type ghCLIConfig map[string]struct {
	User        string `yaml:"user"`
	OauthToken  string `yaml:"oauth_token"`
	GitProtocol string `yaml:"git_protocol"`
}

// Finds the github oath token
func FindToken(githubHost string) string {
	// Try environment variable first
	token := os.Getenv("GITHUB_TOKEN")
	if token != "" {
		return token
	}

	// Try ~/.config/gh/hosts.yml
	cfg, err := readGhCLIConfig()
	if err != nil {
		log.Warn().Err(err).Msg("failed to read gh cli config file")
	} else {
		for host, user := range *cfg {
			if host == githubHost {
				return user.OauthToken
			}
		}
	}

	// Try ~/.config/hub
	hubCfg, err := readHubCLIConfig()
	if err != nil {
		log.Warn().Err(err).Msg("failed to read hub config file")
		return ""
	}

	if c, ok := hubCfg["github.com"]; ok {
		if len(c) == 0 {
			log.Warn().Msg("no token found in hub config file")
			return ""
		}
		if len(c) > 1 {
			log.Warn().Msgf("multiple tokens found in hub config file, using first one: %s", c[0].User)
		}

		return c[0].OauthToken
	}

	return ""
}

func readGhCLIConfig() (*ghCLIConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	f, err := os.Open(path.Join(homeDir, ".config", "gh", "hosts.yml"))
	if err != nil {
		return nil, fmt.Errorf("failed to open gh cli config file: %w", err)
	}

	var cfg ghCLIConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse hub config file: %w", err)
	}

	return &cfg, nil
}

// readHubCLIConfig finds and deserialized the config file for
// Github's "hub" CLI (https://hub.github.com/).
func readHubCLIConfig() (hubCLIConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	f, err := os.Open(path.Join(homeDir, ".config", "hub"))
	if err != nil {
		return nil, fmt.Errorf("failed to open hub config file: %w", err)
	}

	var cfg hubCLIConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse hub config file: %w", err)
	}

	return cfg, nil
}
