package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ScopedPushRepository is a physical Git binding, not a worktree requirement.
// The spelling is retained so replacing an alias cannot redirect recovery.
type ScopedPushRepository struct {
	Directory ScopedPushDirectory `json:"directory"`
	Common    ScopedPushDirectory `json:"common"`
}

func scopedPushCommand(dir string, args ...string) (*exec.Cmd, error) {
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
	} {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("scoped push cannot prove repository identity with %s set", key)
		}
	}
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "--no-lazy-fetch", "--no-replace-objects", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0")
	return cmd, nil
}

// Git's stderr can contain authenticated URLs, including on local config
// failures. Only the operation and exit status cross this boundary.
func scopedPushGit(dir string, args ...string) ([]byte, error) {
	cmd, err := scopedPushCommand(dir, args...)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if exit.ExitCode() == 129 {
				return nil, fmt.Errorf("scoped push requires Git 2.45 or newer with --no-lazy-fetch support: %w", exit)
			}
			return nil, fmt.Errorf("scoped push Git operation failed (exit %d): %w", exit.ExitCode(), exit)
		}
		return nil, errors.New("scoped push could not start Git")
	}
	return out, nil
}

func scopedPushExit(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

func ScopedPushRepositoryAt(path string) (ScopedPushRepository, error) {
	dir, err := scopedPushDirectoryAt(path)
	if err != nil {
		return ScopedPushRepository{}, err
	}
	out, err := scopedPushGit(dir.Path, "rev-parse", "--path-format=absolute", "--git-common-dir", "--show-toplevel")
	if err != nil {
		// Bare repositories have no worktree root. A non-bare administrative
		// directory is not a supported publication execution context.
		bare, bareErr := scopedPushGit(dir.Path, "rev-parse", "--path-format=absolute", "--is-bare-repository", "--git-common-dir")
		parts := strings.Split(strings.TrimSuffix(string(bare), "\n"), "\n")
		if bareErr != nil || len(parts) != 2 || parts[0] != "true" {
			return ScopedPushRepository{}, fmt.Errorf("cannot verify selected repository root at %s: %w", dir.Path, err)
		}
		out = []byte(parts[1] + "\n" + parts[1] + "\n")
	}
	parts := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(parts) != 2 {
		return ScopedPushRepository{}, errors.New("Git returned an ambiguous repository root")
	}
	commonPath, executionPath := parts[0], parts[1]
	if !filepath.IsAbs(commonPath) || !filepath.IsAbs(executionPath) || strings.ContainsAny(commonPath+executionPath, "\r\n\x00") {
		return ScopedPushRepository{}, errors.New("Git returned an invalid repository root")
	}
	execution, err := scopedPushDirectoryAt(executionPath)
	if err != nil {
		return ScopedPushRepository{}, err
	}
	if execution.Path != dir.Path || execution.Identity != dir.Identity {
		return ScopedPushRepository{}, fmt.Errorf("selected repository %s is a subdirectory; scoped push requires the working-tree or bare repository root", dir.Spelling)
	}
	common, err := scopedPushDirectoryAt(commonPath)
	if err != nil {
		return ScopedPushRepository{}, err
	}
	return ScopedPushRepository{Directory: dir, Common: common}, nil
}

func (repo ScopedPushRepository) verify() error {
	if err := errors.Join(repo.Directory.verify(), repo.Common.verify()); err != nil {
		return err
	}
	current, err := ScopedPushRepositoryAt(repo.Directory.Spelling)
	if err != nil {
		return err
	}
	if !current.Directory.sameBinding(repo.Directory) || !current.Common.sameBinding(repo.Common) {
		return errors.New("scoped push repository/common-directory binding changed")
	}
	return nil
}

func scopedPushValidRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || strings.HasSuffix(ref, "/") ||
		strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") ||
		strings.Contains(ref, "@{") || strings.ContainsAny(ref, " ~^:?*[\\\x7f") {
		return false
	}
	for _, c := range ref {
		if c < 0x20 {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func scopedPushRef(dir, ref string) (string, error) {
	if !scopedPushValidRef(ref) {
		return "", fmt.Errorf("invalid full publication ref %q", ref)
	}
	if _, err := scopedPushGit(dir, "symbolic-ref", "-q", ref); err == nil {
		return "", fmt.Errorf("symbolic publication ref %s is unsupported", ref)
	} else if !scopedPushExit(err, 1) {
		return "", err
	}
	out, err := scopedPushGit(dir, "show-ref", "--verify", "--hash", ref)
	if scopedPushExit(err, 1) {
		return "", nil
	}
	if err != nil {
		// show-ref uses 128 for an absent exact ref on older Git versions.
		out, probeErr := scopedPushGit(dir, "for-each-ref", "--format=%(refname)%09%(objectname)", ref)
		if probeErr != nil || len(out) != 0 {
			return "", err
		}
		return "", nil
	}
	oid := strings.TrimSpace(string(out))
	if !validReparentRemoteOID(oid) {
		return "", errors.New("Git returned an invalid object ID")
	}
	return oid, nil
}

func scopedPushCommit(dir, oid string) error {
	if !validReparentRemoteOID(oid) {
		return errors.New("publication requires a full commit object ID")
	}
	out, err := scopedPushGit(dir, "cat-file", "-t", oid)
	if err != nil || strings.TrimSpace(string(out)) != "commit" {
		return errors.New("publication commit is unavailable locally (lazy fetching is disabled)")
	}
	return nil
}

func scopedPushAncestor(dir, ancestor, tip string) (bool, error) {
	if err := errors.Join(scopedPushCommit(dir, ancestor), scopedPushCommit(dir, tip)); err != nil {
		return false, err
	}
	_, err := scopedPushGit(dir, "merge-base", "--is-ancestor", ancestor, tip)
	if scopedPushExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

type scopedPushEndpoint struct {
	URL         string
	Fingerprint string
	TrackingRef string
}

func scopedPushConfigValues(dir, key string) ([]string, error) {
	out, err := scopedPushGit(dir, "config", "--null", "--get-all", key)
	if scopedPushExit(err, 1) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}

func scopedPushEndpointURL(dir string, push bool) (string, error) {
	args := []string{"remote", "get-url"}
	if push {
		args = append(args, "--push")
	}
	out, err := scopedPushGit(dir, append(args, "--all", "origin")...)
	if err != nil {
		return "", errors.New("selected repository has no usable origin endpoint")
	}
	raw := strings.TrimSuffix(string(out), "\n")
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("origin must have exactly one unambiguous endpoint")
	}
	if strings.HasPrefix(raw, "-") || strings.Contains(raw, "::") {
		return "", errors.New("unsupported origin endpoint")
	}
	// Directory is proved to be Git's execution root, not merely a directory
	// inside a repository. Only that root may anchor a relative origin URL.
	if !strings.Contains(raw, "://") && !strings.Contains(raw, ":") {
		path, err := scopedPushLocalEndpointPath(dir, raw)
		if err != nil {
			return "", err
		}
		raw = path
	}
	return raw, nil
}

func scopedPushResolveEndpoint(dir, destination string) (scopedPushEndpoint, error) {
	push, err := scopedPushEndpointURL(dir, true)
	if err != nil {
		return scopedPushEndpoint{}, err
	}
	fetch, err := scopedPushEndpointURL(dir, false)
	if err != nil {
		return scopedPushEndpoint{}, err
	}
	if push != fetch {
		return scopedPushEndpoint{}, errors.New("distinct origin fetch/push endpoints cannot prove tracking authority")
	}
	if err := scopedPushCheckLiteralEndpoint(dir, push); err != nil {
		return scopedPushEndpoint{}, err
	}
	mappings, err := scopedPushConfigValues(dir, "remote.origin.fetch")
	if err != nil {
		return scopedPushEndpoint{}, err
	}
	var tracking string
	for _, mapping := range mappings {
		source, target, ok := strings.Cut(strings.TrimPrefix(mapping, "+"), ":")
		if !ok || strings.HasPrefix(source, "^") {
			return scopedPushEndpoint{}, errors.New("unsupported origin tracking mapping")
		}
		match := source == destination
		if strings.Contains(source, "*") || strings.Contains(target, "*") {
			if source != "refs/heads/*" || target != "refs/remotes/origin/*" {
				return scopedPushEndpoint{}, errors.New("unsupported origin wildcard tracking mapping")
			}
			target = "refs/remotes/origin/" + strings.TrimPrefix(destination, "refs/heads/")
			match = true
		}
		if !match {
			continue
		}
		if tracking != "" || !strings.HasPrefix(target, "refs/remotes/origin/") || !scopedPushValidRef(target) {
			return scopedPushEndpoint{}, errors.New("ambiguous origin tracking mapping")
		}
		tracking = target
	}
	if tracking == "" {
		return scopedPushEndpoint{}, errors.New("no supported origin tracking mapping for selected destination")
	}
	sum := sha256.Sum256([]byte("tws-push-endpoint-v1\x00" + push + "\x00" + tracking + "\x00" + strings.Join(mappings, "\x00")))
	return scopedPushEndpoint{URL: push, TrackingRef: tracking, Fingerprint: hex.EncodeToString(sum[:])}, nil
}

func scopedPushLocalEndpointPath(dir, raw string) (string, error) {
	path := raw
	if !filepath.IsAbs(path) {
		path = dir + string(filepath.Separator) + path
	}
	parts := strings.Split(path, string(filepath.Separator))
	lastParent := -1
	for i, part := range parts {
		if part == ".." {
			lastParent = i
		}
	}
	current := string(filepath.Separator)
	for i, part := range parts {
		if i > lastParent {
			break
		}
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
		default:
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("local origin has an unverifiable or symlink-dependent parent traversal; use an unambiguous endpoint")
			}
		}
	}
	return filepath.Clean(path), nil
}

func scopedPushCheckLiteralEndpoint(dir, endpoint string) error {
	// Ask Git to parse the complete key: its variable component is
	// case-insensitive, unlike a remote-name subsection or a regexp match.
	groups, err := scopedPushConfigValues(dir, "remotes."+endpoint)
	if err != nil {
		return err
	}
	if len(groups) != 0 {
		return errors.New("resolved origin endpoint is also a configured remote group; refusing ambiguous transport")
	}
	_, err = scopedPushGit(dir, "config", "--null", "--get-regexp", `^remote\.`+regexp.QuoteMeta(endpoint)+`\.`)
	if err == nil {
		return errors.New("resolved origin endpoint is also a configured remote name; refusing ambiguous transport")
	}
	if !scopedPushExit(err, 1) {
		return err
	}
	return scopedPushCheckEndpointRewrites(dir, endpoint)
}

// get-url expands a named remote once. Sending that resolved URL to push or
// ls-remote would expand it AGAIN. Refuse any non-identity rule applicable to
// the pinned endpoint instead of silently publishing to a different server.
func scopedPushCheckEndpointRewrites(dir, endpoint string) error {
	out, err := scopedPushGit(dir, "config", "--null", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if scopedPushExit(err, 1) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, item := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		key, prefix, ok := strings.Cut(item, "\n")
		if !ok || !strings.HasPrefix(key, "url.") {
			return errors.New("unverifiable origin URL rewrite configuration")
		}
		replacement := strings.TrimPrefix(key, "url.")
		if !strings.HasSuffix(replacement, ".insteadof") && !strings.HasSuffix(replacement, ".pushinsteadof") {
			return errors.New("unsupported origin URL rewrite configuration")
		}
		if value, ok := strings.CutSuffix(replacement, ".pushinsteadof"); ok {
			replacement = value
		} else {
			replacement = strings.TrimSuffix(replacement, ".insteadof")
		}
		if strings.HasPrefix(endpoint, prefix) && replacement+strings.TrimPrefix(endpoint, prefix) != endpoint {
			return errors.New("origin endpoint would be rewritten again; use an unambiguous origin endpoint configuration")
		}
	}
	return nil
}

type ScopedPushReflogTip struct {
	OID  string `json:"oid"`
	Time int64  `json:"time"`
}

type ScopedPushInclusion struct {
	TrackingTime int64                 `json:"tracking_time"`
	Tips         []ScopedPushReflogTip `json:"tips"`
	Witness      string                `json:"witness"`
}

func scopedPushReflog(dir, ref string) ([]ScopedPushReflogTip, error) {
	out, err := scopedPushGit(dir, "reflog", "show", "--date=unix", "--format=%H%x09%gD", ref)
	if err != nil {
		return nil, err
	}
	var tips []ScopedPushReflogTip
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		oid, selector, ok := strings.Cut(line, "\t")
		i := strings.LastIndex(selector, "@{")
		if !ok || !validReparentRemoteOID(oid) || i < 0 || !strings.HasSuffix(selector, "}") {
			return nil, errors.New("inclusion requires a readable local reflog")
		}
		stamp, err := strconv.ParseInt(selector[i+2:len(selector)-1], 10, 64)
		if err != nil || stamp <= 0 {
			return nil, errors.New("inclusion reflog has an invalid timestamp")
		}
		tips = append(tips, ScopedPushReflogTip{OID: oid, Time: stamp})
	}
	return tips, nil
}

// An explicit expected-OID lease disables Git's --force-if-includes. This
// independent witness uses the same bounded range of branch reflog NEW tips:
// newest through the first tip older than the latest tracking-ref timestamp.
// No older history grants authority, and missing evidence always refuses.
func scopedPushProveInclusion(dir, branchRef, trackingRef, expected, source string) (*ScopedPushInclusion, error) {
	if expected == "" {
		return nil, errors.New("reparent inclusion requires existing tracking evidence")
	}
	tracking, err := scopedPushReflog(dir, trackingRef)
	if err != nil || len(tracking) == 0 || tracking[0].OID != expected {
		return nil, errors.New("reparent inclusion requires the current tracking reflog tip")
	}
	branch, err := scopedPushReflog(dir, branchRef)
	if err != nil || len(branch) == 0 || branch[0].OID != source {
		return nil, errors.New("reparent inclusion requires the current branch reflog tip")
	}
	proof := &ScopedPushInclusion{TrackingTime: tracking[0].Time}
	for _, tip := range branch {
		proof.Tips = append(proof.Tips, tip)
		contains, err := scopedPushAncestor(dir, expected, tip.OID)
		if err != nil {
			return nil, err
		}
		if contains && proof.Witness == "" {
			proof.Witness = tip.OID
		}
		if tip.Time < proof.TrackingTime {
			break
		}
	}
	if proof.Witness == "" {
		return nil, errors.New("reparent inclusion refused: no qualifying branch reflog tip includes the frozen remote-tracking tip; integrate remote work before starting a new push")
	}
	return proof, nil
}

func scopedPushPublish(dir, endpoint string, entry ScopedPushEntry) error {
	if err := scopedPushCheckLiteralEndpoint(dir, endpoint); err != nil {
		return err
	}
	args := []string{
		"-c", "push.followTags=false", "-c", "push.recurseSubmodules=no",
		"-c", "push.autoSetupRemote=false", "-c", "remote.origin.mirror=false",
		"push", "--porcelain", "--no-mirror", "--no-all", "--no-tags",
		"--no-follow-tags", "--no-prune", "--recurse-submodules=no",
		"--force-with-lease=" + entry.Destination + ":" + entry.Expected,
		"--", endpoint, entry.Source + ":" + entry.Destination,
	}
	cmd, err := scopedPushCommand(dir, args...)
	if err != nil {
		return err
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &output, &output, os.Stdin
	if err := cmd.Run(); err != nil {
		return errors.New("Git push failed; remote effect is uncertain")
	}
	return nil
}

func scopedPushRemoteValue(dir, endpoint, ref string) (string, error) {
	if err := scopedPushCheckLiteralEndpoint(dir, endpoint); err != nil {
		return "", err
	}
	out, err := scopedPushGit(dir, "ls-remote", "--refs", "--exit-code", "--", endpoint, ref)
	if scopedPushExit(err, 2) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("cannot reconcile the recorded remote destination")
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[1] != ref || !validReparentRemoteOID(fields[0]) {
		return "", errors.New("remote returned ambiguous destination evidence")
	}
	return fields[0], nil
}

func scopedPushRefDirectories(repo ScopedPushRepository, ref string) error {
	path := repo.Common.Path
	for _, part := range strings.Split(filepath.Dir(ref), "/") {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("publication private-ref namespace contains a non-directory or symlink")
		}
	}
	return nil
}

func scopedPushSyncRegular(path string) error {
	snapshot, err := scopedPushReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, snapshot.info) {
		return errors.Join(err, file.Close(), errors.New("publication evidence changed before durability barrier"))
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return snapshot.verify(path)
}

// Repeat reference-storage barriers after uncertain local ref effects too.
// Git's expected-value update remains the only writer of reference contents.
func scopedPushSyncRefStorage(repo ScopedPushRepository, pin ScopedPushPin) error {
	if err := errors.Join(repo.verify(), scopedPushRefDirectories(repo, pin.Ref)); err != nil {
		return err
	}
	if err := errors.Join(
		scopedPushSyncRegular(filepath.Join(repo.Common.Path, pin.Ref)),
		scopedPushSyncRegular(filepath.Join(repo.Common.Path, "packed-refs")),
	); err != nil {
		return err
	}
	tables := filepath.Join(repo.Common.Path, "reftable")
	if info, err := os.Lstat(tables); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unverifiable reftable directory")
		}
		entries, err := os.ReadDir(tables)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".lock") {
				return errors.New("Git reference storage is busy; preserve publication state and retry")
			}
			if err := scopedPushSyncRegular(filepath.Join(tables, entry.Name())); err != nil {
				return err
			}
		}
		if err := syncDir(tables); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := scopedPushCheckpoint("before-private-ref-directory-sync", pin.Entry); err != nil {
		return err
	}
	for path := filepath.Join(repo.Common.Path, filepath.Dir(pin.Ref)); ; path = filepath.Dir(path) {
		if _, err := os.Lstat(path); err == nil {
			if err := syncDir(path); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if path == repo.Common.Path {
			break
		}
	}
	return repo.verify()
}
