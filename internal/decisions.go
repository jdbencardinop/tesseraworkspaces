package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Decision struct {
	ID        int    `yaml:"id"`
	Branch    string `yaml:"branch"`
	Timestamp string `yaml:"timestamp"`
	Type      string `yaml:"type"` // breaking, info, deprecation
	Summary   string `yaml:"summary"`
	Details   string `yaml:"details,omitempty"`
}

type Decisions struct {
	Entries []Decision `yaml:"entries"`
}

func DecisionsPath(featurePath string) string {
	return filepath.Join(featurePath, "decisions.yaml")
}

func LoadDecisions(featurePath string) (Decisions, error) {
	data, err := os.ReadFile(DecisionsPath(featurePath))
	if err != nil {
		return Decisions{}, err
	}
	var d Decisions
	if err := yaml.Unmarshal(data, &d); err != nil {
		return Decisions{}, err
	}
	return d, nil
}

func SaveDecisions(featurePath string, d Decisions) error {
	data, err := yaml.Marshal(&d)
	if err != nil {
		return err
	}
	return os.WriteFile(DecisionsPath(featurePath), data, 0644)
}

func AddDecision(featurePath, branch, summary, decisionType, details string) (Decision, error) {
	decisions, _ := LoadDecisions(featurePath)

	// Auto-increment ID
	maxID := 0
	for _, e := range decisions.Entries {
		if e.ID > maxID {
			maxID = e.ID
		}
	}

	entry := Decision{
		ID:        maxID + 1,
		Branch:    branch,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      decisionType,
		Summary:   summary,
		Details:   details,
	}

	decisions.Entries = append(decisions.Entries, entry)

	if err := SaveDecisions(featurePath, decisions); err != nil {
		return Decision{}, err
	}
	return entry, nil
}

func (d Decision) String() string {
	typeTag := fmt.Sprintf("[%s]", d.Type)
	s := fmt.Sprintf("#%d %s %s (%s, %s)", d.ID, typeTag, d.Summary, d.Branch, d.Timestamp)
	if d.Details != "" {
		s += fmt.Sprintf("\n    %s", d.Details)
	}
	return s
}
