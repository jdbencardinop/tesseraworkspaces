package internal

import (
	"time"

	"gopkg.in/yaml.v3"
)

type WorkspaceExport struct {
	Feature    string    `yaml:"feature"`
	ExportedAt string    `yaml:"exported_at"`
	Stack      Stack     `yaml:"stack"`
	Decisions  Decisions `yaml:"decisions"`
}

func NewWorkspaceExport(feature string, stack Stack, decisions Decisions) WorkspaceExport {
	return WorkspaceExport{
		Feature:    feature,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Stack:      stack,
		Decisions:  decisions,
	}
}

func MarshalExport(export WorkspaceExport) ([]byte, error) {
	return yaml.Marshal(&export)
}

func UnmarshalExport(data []byte) (WorkspaceExport, error) {
	var export WorkspaceExport
	if err := yaml.Unmarshal(data, &export); err != nil {
		return WorkspaceExport{}, err
	}
	return export, nil
}
