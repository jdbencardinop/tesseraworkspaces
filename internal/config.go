package internal

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Workspaces   map[string]string `yaml:"workspaces"`
	AgentCommand string            `yaml:"agent_command"`
	UseTmux      *bool             `yaml:"use_tmux"`
}

func (c Config) GetAgentCommand() string {
	if c.AgentCommand != "" {
		return c.AgentCommand
	}
	return "claude"
}

// ConfigPath returns the global config path.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tws", "config.yaml")
}

func repoConfigPath() string {
	root, err := RepoRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, ".tws", "config.yaml")
}

func loadConfigFile(path string) Config {
	if path == "" {
		return Config{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}
	}
	return cfg
}

// LoadConfig loads the global config, then merges per-repo config on top.
// Per-repo values override global values when set.
func LoadConfig() Config {
	cfg := loadConfigFile(ConfigPath())
	repo := loadConfigFile(repoConfigPath())

	if repo.AgentCommand != "" {
		cfg.AgentCommand = repo.AgentCommand
	}
	if repo.UseTmux != nil {
		cfg.UseTmux = repo.UseTmux
	}
	if len(repo.Workspaces) > 0 {
		if cfg.Workspaces == nil {
			cfg.Workspaces = make(map[string]string)
		}
		for k, v := range repo.Workspaces {
			cfg.Workspaces[k] = v
		}
	}

	return cfg
}
