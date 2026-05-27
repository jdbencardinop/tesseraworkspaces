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
	InjectInto   string            `yaml:"inject_into"`
	TestCommand  string            `yaml:"test_command"`
	AutoHooks    *bool             `yaml:"auto_hooks"`
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
	if repo.InjectInto != "" {
		cfg.InjectInto = repo.InjectInto
	}
	if repo.TestCommand != "" {
		cfg.TestCommand = repo.TestCommand
	}
	if repo.AutoHooks != nil {
		cfg.AutoHooks = repo.AutoHooks
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

// SaveConfigFile writes a config to the given path.
func SaveConfigFile(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// RepoConfigPath exports the per-repo config path for the config command.
func RepoConfigPath() string {
	return repoConfigPath()
}

// LoadConfigFile loads a single config file (exported for the config command).
func LoadConfigFile(path string) Config {
	return loadConfigFile(path)
}

// TemplatePath returns the template directory path.
// Per-repo (.tws/templates/) takes priority over global (~/.config/tws/templates/).
// Returns empty string if neither exists.
func TemplatePath() string {
	// Per-repo first
	repoPath := repoConfigPath()
	if repoPath != "" {
		repoTemplates := filepath.Join(filepath.Dir(repoPath), "templates")
		if info, err := os.Stat(repoTemplates); err == nil && info.IsDir() {
			return repoTemplates
		}
	}

	// Global
	home, _ := os.UserHomeDir()
	globalTemplates := filepath.Join(home, ".config", "tws", "templates")
	if info, err := os.Stat(globalTemplates); err == nil && info.IsDir() {
		return globalTemplates
	}

	return ""
}
