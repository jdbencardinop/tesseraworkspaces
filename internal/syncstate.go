package internal

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type SyncState struct {
	StartedAt    string   `yaml:"started_at"`
	FailedBranch string   `yaml:"failed_branch"`
	Pending      []string `yaml:"pending"`
	Completed    []string `yaml:"completed"`
	Skipped      []string `yaml:"skipped"`
}

func SyncStatePath(featurePath string) string {
	return filepath.Join(featurePath, ".sync-state.yaml")
}

func LoadSyncState(featurePath string) (*SyncState, error) {
	data, err := os.ReadFile(SyncStatePath(featurePath))
	if err != nil {
		return nil, err
	}
	var s SyncState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SaveSyncState(featurePath string, s *SyncState) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(SyncStatePath(featurePath), data, 0644)
}

func DeleteSyncState(featurePath string) {
	os.Remove(SyncStatePath(featurePath)) //nolint:errcheck
}

func HasSyncState(featurePath string) bool {
	_, err := os.Stat(SyncStatePath(featurePath))
	return err == nil
}

func NewSyncState() *SyncState {
	return &SyncState{
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
}
