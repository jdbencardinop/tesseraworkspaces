package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Workspaces           map[string]string `yaml:"workspaces"`
	AgentCommand         string            `yaml:"agent_command"`
	UseTmux              *bool             `yaml:"use_tmux"`
	InjectInto           string            `yaml:"inject_into"`
	TestCommand          string            `yaml:"test_command"`
	AutoHooks            *bool             `yaml:"auto_hooks"`
	BranchPrefix         string            `yaml:"branch_prefix"`
	WorkspaceMode        string            `yaml:"workspace_mode,omitempty"`
	WorktreeTemplate     string            `yaml:"worktree_template,omitempty"`
	OrchestratorTemplate string            `yaml:"orchestrator_template,omitempty"`
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

// LoadConfigFileE is the strict config loader used by managed template
// operations. Legacy callers retain the historical best-effort loader.
func LoadConfigFileE(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, fmt.Errorf("reading config %s: config must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return cfg, nil
}

func mergeConfig(base, repo Config) Config {
	cfg := base
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
	if repo.BranchPrefix != "" {
		cfg.BranchPrefix = repo.BranchPrefix
	}
	if repo.WorkspaceMode != "" {
		cfg.WorkspaceMode = repo.WorkspaceMode
	}
	if repo.WorktreeTemplate != "" {
		cfg.WorktreeTemplate = repo.WorktreeTemplate
	}
	if repo.OrchestratorTemplate != "" {
		cfg.OrchestratorTemplate = repo.OrchestratorTemplate
	}
	if len(repo.Workspaces) > 0 {
		if cfg.Workspaces == nil {
			cfg.Workspaces = make(map[string]string)
		}
		maps.Copy(cfg.Workspaces, repo.Workspaces)
	}
	return cfg
}

// LoadConfig loads the global config, then merges per-repo config on top.
// Per-repo values override global values when set.
func LoadConfig() Config {
	return mergeConfig(loadConfigFile(ConfigPath()), loadConfigFile(repoConfigPath()))
}

func LoadConfigForRepo(repoRoot string) Config {
	return mergeConfig(loadConfigFile(ConfigPath()), loadConfigFile(RepoConfigPathFor(repoRoot)))
}

// LoadConfigForRepoE loads global and repository configuration without
// suppressing malformed or unreadable files.
func LoadConfigForRepoE(repoRoot string) (Config, error) {
	global, err := LoadConfigFileE(ConfigPath())
	if err != nil {
		return Config{}, err
	}
	repo, err := LoadConfigFileE(RepoConfigPathFor(repoRoot))
	if err != nil {
		return Config{}, err
	}
	return mergeConfig(global, repo), nil
}

// LoadTemplateConfigLayersE returns the independent layers so role defaults
// can honor explicit repository-before-global precedence.
func LoadTemplateConfigLayersE(repoRoot string) (global Config, repo Config, err error) {
	global, err = LoadConfigFileE(ConfigPath())
	if err != nil {
		return Config{}, Config{}, err
	}
	repo, err = LoadConfigFileE(RepoConfigPathFor(repoRoot))
	if err != nil {
		return Config{}, Config{}, err
	}
	return global, repo, nil
}

func RepoConfigPathFor(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	return filepath.Join(repoRoot, ".tws", "config.yaml")
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

// LoadRepoConfig loads a per-repo config from the given path.
func LoadRepoConfig(path string) Config {
	return loadConfigFile(path)
}

// SaveRepoConfig writes a per-repo config to the given path.
func SaveRepoConfig(path string, cfg Config) error {
	return SaveConfigFile(path, cfg)
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
