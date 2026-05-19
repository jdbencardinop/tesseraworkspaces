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
	To        string `yaml:"to,omitempty"` // targeted recipient branch (empty = broadcast)
	Timestamp string `yaml:"timestamp"`
	Type      string `yaml:"type"` // breaking, info, deprecation, review, question
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

func AddDecision(featurePath, branch, to, summary, decisionType, details string) (Decision, error) {
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
		To:        to,
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
	target := ""
	if d.To != "" {
		target = fmt.Sprintf(" → %s", d.To)
	}
	s := fmt.Sprintf("#%d %s %s (%s%s, %s)", d.ID, typeTag, d.Summary, d.Branch, target, d.Timestamp)
	if d.Details != "" {
		s += fmt.Sprintf("\n    %s", d.Details)
	}
	return s
}

// IsRelevantTo returns true if a decision should be seen by the given branch.
// Broadcast decisions (empty To) are relevant to everyone.
// Targeted decisions are only relevant to the specified branch.
func (d Decision) IsRelevantTo(branch string) bool {
	if d.To == "" {
		return true // broadcast
	}
	return d.To == branch
}

// --- Read state tracking ---

type ReadState struct {
	Branches map[string]int `yaml:"branches"`
}

func readStatePath(featurePath string) string {
	return filepath.Join(featurePath, "read-state.yaml")
}

func LoadReadState(featurePath string) ReadState {
	data, err := os.ReadFile(readStatePath(featurePath))
	if err != nil {
		return ReadState{Branches: make(map[string]int)}
	}
	var rs ReadState
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return ReadState{Branches: make(map[string]int)}
	}
	if rs.Branches == nil {
		rs.Branches = make(map[string]int)
	}
	return rs
}

func SaveReadState(featurePath string, rs ReadState) error {
	data, err := yaml.Marshal(&rs)
	if err != nil {
		return err
	}
	return os.WriteFile(readStatePath(featurePath), data, 0644)
}

// AckDecisions marks all current decisions as read for the given branch.
func AckDecisions(featurePath, branch string) error {
	decisions, err := LoadDecisions(featurePath)
	if err != nil {
		return err
	}

	maxID := 0
	for _, d := range decisions.Entries {
		if d.ID > maxID {
			maxID = d.ID
		}
	}

	rs := LoadReadState(featurePath)
	rs.Branches[branch] = maxID
	return SaveReadState(featurePath, rs)
}

// LastReadID returns the last read decision ID for a branch.
func LastReadID(featurePath, branch string) int {
	rs := LoadReadState(featurePath)
	return rs.Branches[branch]
}

// UnreadDecisions returns decisions relevant to a branch that haven't been acked.
func UnreadDecisions(featurePath, branch string) []Decision {
	decisions, err := LoadDecisions(featurePath)
	if err != nil {
		return nil
	}

	lastRead := LastReadID(featurePath, branch)

	var unread []Decision
	for _, d := range decisions.Entries {
		if d.ID > lastRead && d.IsRelevantTo(branch) {
			unread = append(unread, d)
		}
	}
	return unread
}
