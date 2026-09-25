package internal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const externalSessionIntentSchema = 1

const (
	ExternalSessionIntentBranches = "branches"
	ExternalSessionIntentFeature  = "feature"
)

const externalSessionIntentsDirName = ".session-intents"

type ExternalSessionIntent struct {
	SchemaVersion int      `json:"schema_version"`
	Token         string   `json:"token"`
	Feature       string   `json:"feature"`
	Scope         string   `json:"scope"`
	Names         []string `json:"names,omitempty"`
	OwnerPID      int      `json:"owner_pid"`
	CreatedAt     string   `json:"created_at"`
}

type LoadedExternalSessionIntent struct {
	Record  ExternalSessionIntent
	File    string
	State   DirectRecordState
	Problem string
}

func ExternalSessionIntentsDir(featurePath string) string {
	return filepath.Join(featurePath, externalSessionIntentsDirName)
}

func externalSessionIntentFile(featurePath, token string) string {
	return filepath.Join(ExternalSessionIntentsDir(featurePath), token+".json")
}

func CreateExternalSessionIntent(featurePath, feature string, names []string, featureWide bool) (string, error) {
	if featurePath == "" || feature == "" {
		return "", fmt.Errorf("external session intent requires a feature path and feature")
	}
	scope := ExternalSessionIntentBranches
	names = normalizedExternalSessionIntentNames(names)
	if featureWide {
		scope = ExternalSessionIntentFeature
		names = nil
	} else if len(names) == 0 {
		return "", fmt.Errorf("branch-scoped external session intent requires at least one branch name")
	}
	token, err := newExternalSessionIntentToken()
	if err != nil {
		return "", err
	}
	dir := ExternalSessionIntentsDir(featurePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating external session intent directory: %w", err)
	}
	rec := ExternalSessionIntent{
		SchemaVersion: externalSessionIntentSchema,
		Token:         token,
		Feature:       feature,
		Scope:         scope,
		Names:         names,
		OwnerPID:      os.Getpid(),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	path := externalSessionIntentFile(featurePath, token)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating external session intent: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", errors.Join(writeErr, closeErr)
	}
	return token, nil
}

func RemoveOwnedExternalSessionIntent(featurePath, token string) error {
	path := externalSessionIntentFile(featurePath, token)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		pruneExternalSessionIntentDir(featurePath)
		return nil
	}
	if err != nil {
		return err
	}
	var rec ExternalSessionIntent
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("parsing external session intent: %w", err)
	}
	if rec.Token != token {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	pruneExternalSessionIntentDir(featurePath)
	return nil
}

func LoadExternalSessionIntents(featurePath, feature string, affected []string, proc ProcessProber) (blocking, stale []LoadedExternalSessionIntent, err error) {
	if proc == nil {
		proc = NewProcessProber()
	}
	dir := ExternalSessionIntentsDir(featurePath)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading external session intent directory: %w", err)
	}
	affectedSet := make(map[string]bool, len(affected))
	for _, name := range affected {
		affectedSet[name] = true
	}
	for _, entry := range entries {
		if entry.IsDir() || !directRecordFileRe.MatchString(entry.Name()) {
			blocking = append(blocking, LoadedExternalSessionIntent{
				File: filepath.Join(dir, entry.Name()), State: DirectRecordInvalid,
				Problem: "unexpected entry in external session intent directory",
			})
			continue
		}
		path := filepath.Join(dir, entry.Name())
		loaded := LoadedExternalSessionIntent{File: path, State: DirectRecordOK}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			loaded.State = DirectRecordInvalid
			loaded.Problem = "intent is not a verifiable regular file"
			blocking = append(blocking, loaded)
			continue
		}
		data, readErr := os.ReadFile(path)
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}
		if readErr != nil {
			loaded.State = DirectRecordInvalid
			loaded.Problem = "unreadable: " + readErr.Error()
			blocking = append(blocking, loaded)
			continue
		}
		if err := json.Unmarshal(data, &loaded.Record); err != nil {
			loaded.State = DirectRecordInvalid
			loaded.Problem = "unparseable: " + err.Error()
			blocking = append(blocking, loaded)
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".json")
		switch {
		case loaded.Record.SchemaVersion != externalSessionIntentSchema:
			loaded.State = DirectRecordUnsupported
			loaded.Problem = fmt.Sprintf("schema version %d is not supported", loaded.Record.SchemaVersion)
		case loaded.Record.Token != stem:
			loaded.State = DirectRecordInvalid
			loaded.Problem = "token mismatch"
		case loaded.Record.Feature != feature:
			loaded.State = DirectRecordInvalid
			loaded.Problem = "feature mismatch"
		case loaded.Record.OwnerPID <= 0:
			loaded.State = DirectRecordInvalid
			loaded.Problem = "invalid owner pid"
		case !validExternalSessionIntentCreatedAt(loaded.Record.CreatedAt):
			loaded.State = DirectRecordInvalid
			loaded.Problem = "invalid created_at"
		case loaded.Record.Scope == ExternalSessionIntentFeature:
		case loaded.Record.Scope == ExternalSessionIntentBranches && validExternalSessionIntentNames(loaded.Record.Names):
		default:
			loaded.State = DirectRecordInvalid
			loaded.Problem = "invalid scope or branch names"
		}
		if loaded.State != DirectRecordOK {
			blocking = append(blocking, loaded)
			continue
		}
		applies := loaded.Record.Scope == ExternalSessionIntentFeature
		if !applies {
			for _, name := range loaded.Record.Names {
				if affectedSet[name] {
					applies = true
					break
				}
			}
		}
		if !applies {
			continue
		}
		switch proc.Probe(loaded.Record.OwnerPID) {
		case ProcessLive, ProcessUnknown:
			blocking = append(blocking, loaded)
		default:
			stale = append(stale, loaded)
		}
	}
	sort.Slice(blocking, func(i, j int) bool { return blocking[i].File < blocking[j].File })
	sort.Slice(stale, func(i, j int) bool { return stale[i].File < stale[j].File })
	return blocking, stale, nil
}

func RemoveStaleExternalSessionIntents(featurePath string, stale []LoadedExternalSessionIntent) (int, error) {
	removed := 0
	var errs []error
	for _, intent := range stale {
		if intent.Record.Token == "" {
			continue
		}
		if err := RemoveOwnedExternalSessionIntent(featurePath, intent.Record.Token); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func normalizedExternalSessionIntentNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func validExternalSessionIntentNames(names []string) bool {
	if len(names) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || name != strings.TrimSpace(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func validExternalSessionIntentCreatedAt(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func newExternalSessionIntentToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating external session intent token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func pruneExternalSessionIntentDir(featurePath string) {
	_ = os.Remove(ExternalSessionIntentsDir(featurePath))
}
