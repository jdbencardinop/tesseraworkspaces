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

func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tws", "config.yaml")
}

func LoadConfig() Config {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return Config{}
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}
	}
	return cfg
}
