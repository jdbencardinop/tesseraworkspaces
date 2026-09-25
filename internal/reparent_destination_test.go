package internal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2): this file owns the destination and cutoff cells.
//
//	T-004 cutoff ladder: recorded / conflict / unresolvable ....... AC-022, AC-023
//	T-005 cutoff ladder: supplied / old-parent-tip / absent ....... AC-024, AC-025, AC-026
//	T-006 non-ancestor cutoff refuses without rebasing ............ AC-027
//	T-007 descendant cutoff snapshot vs live ref ................. AC-028, AC-029
//	T-008 cutoff ambiguity folds to one kind ..................... AC-030
//	T-010 destination kinds, every literal shape ................. AC-015
//	T-011 destination ambiguity, exact-equality filter ........... AC-014
//	T-012 abbreviated OID via --disambiguate ..................... AC-015
//	T-014 pseudo refs rejected, destination and cutoff ........... AC-017
//	T-015 --onto-kind auto/entry/ref matrix ...................... AC-011, AC-012, AC-013
//	T-016 canonical stored token, short token never persisted .... AC-013, AC-020
//	T-017 four-resolver agreement plus injected divergence ....... AC-021
//	T-018 literal-ref root, no --root flag ....................... AC-018
//	T-019 cycle / self / descendant destinations ................. AC-019
//	T-020 default-branch token is not rewritten .................. AC-020
//	T-021 destination ancestor of the old parent warns, still runs  §7.11
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// reparentRefusalOf asserts err is a reparent refusal of the given kind and
// returns its detail, so a cell can go on to assert the sentence. It returns
// a string rather than the error value so an assertion-only call site is not
// an unchecked error return.
func reparentRefusalOf(t *testing.T, err error, want ReparentRefusalKind) string {
	t.Helper()
	if err == nil {
		t.Fatalf("expected refusal %s, got nil", want)
	}
	var refusal *ReparentRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a reparent refusal", err)
	}
	if refusal.Kind != want {
		t.Fatalf("refusal kind = %s (%s), want %s", refusal.Kind, refusal.Detail, want)
	}
	return refusal.Detail
}

// reparentAssertPublishedResolution checks a refused destination still
// publishes closed-domain facts: resolution is a member of the §7.5b/§7.5
// domain (never the empty string), the requested token is preserved, and the
// candidate list is exactly what the run learned.
func reparentAssertPublishedResolution(t *testing.T, dest ReparentDestination, token, wantResolution string, wantCandidates []string) {
	t.Helper()
	domain := map[string]bool{
		ReparentResolutionEntry: true, ReparentResolutionRefFull: true,
		ReparentResolutionRefHead: true, ReparentResolutionRefTag: true,
		ReparentResolutionRefRemote: true, ReparentResolutionRefOther: true,
		ReparentResolutionRawOID: true, ReparentResolutionUnresolved: true,
		ReparentResolutionAmbiguous: true,
	}
	if !domain[dest.Resolution] {
		t.Fatalf("resolution %q is outside the closed domain", dest.Resolution)
	}
	if dest.Resolution != wantResolution {
		t.Fatalf("resolution = %q, want %q", dest.Resolution, wantResolution)
	}
	if dest.RequestedToken != token {
		t.Fatalf("requested token = %q, want %q", dest.RequestedToken, token)
	}
	if len(dest.Candidates) != len(wantCandidates) {
		t.Fatalf("candidates = %v, want %v", dest.Candidates, wantCandidates)
	}
	for i := range wantCandidates {
		if dest.Candidates[i] != wantCandidates[i] {
			t.Fatalf("candidate %d = %q, want %q (sorted)", i, dest.Candidates[i], wantCandidates[i])
		}
	}
	if dest.SHA != "" {
		t.Fatalf("a refused destination must pin no SHA, got %q", dest.SHA)
	}
}

// reparentBulkObjects writes two frozen blob payloads whose Git SHA-1 object
// ids share the 4-hex prefix 9abe. The fixture is deterministic rather than a
// birthday-probability search that can silently skip a required cell.
func reparentBulkObjects(t *testing.T, repo *reparentRepo, count int) []string {
	t.Helper()
	_ = count
	dir := filepath.Join(repo.Dir, "bulk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payloads := []string{"reparent collision object 106\n", "reparent collision object 142\n"}
	paths := make([]string, 0, len(payloads))
	for i, payload := range payloads {
		name := filepath.Join(dir, fmt.Sprintf("blob-%04d.txt", i))
		if err := os.WriteFile(name, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	cmd := reparentTestGitCommand(t, repo.Dir, append([]string{"hash-object", "-w"}, paths...)...)
	cmd.Env = append(cmd.Env, "HOME="+repo.Dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bulk hash-object: %v\n%s", err, out)
	}
	var oids []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			oids = append(oids, strings.ToLower(line))
		}
	}
	return oids
}

// reparentCollidingPrefix finds a 4-hex prefix shared by at least two objects.
func reparentCollidingPrefix(t *testing.T, oids []string) string {
	t.Helper()
	seen := make(map[string]int, len(oids))
	for _, oid := range oids {
		prefix := oid[:4]
		seen[prefix]++
		if seen[prefix] > 1 {
			return prefix
		}
	}
	t.Fatalf("the frozen collision fixture produced no shared 4-hex prefix: %v", oids)
	return "" // unreachable
}

func reparentWidth(t *testing.T, repo *reparentRepo) int {
	t.Helper()
	width, err := reparentOIDWidth(repo.Dir)
	if err != nil {
		t.Fatalf("reparentOIDWidth: %v", err)
	}
	return width
}

// ---------------------------------------------------------------------------
// OID width, candidate enumeration, ancestry primitive
// ---------------------------------------------------------------------------

// T-010, T-011, T-012: the OID-width probe, the five-candidate enumeration and the ancestor trichotomy (AC-014, AC-015).
//
// §17.3 counts one t.Run leaf per real-Git cell, so the assertions below are
// grouped into a single leaf. Each original test keeps its own scope, its own
// repository and its own assertions verbatim; only the leaf boundary moved.
func TestReparentDestinationPrimitives(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-010", "destination-primitives")
	repo := newReparentPrimitiveRepo(t)
	// --- TestReparentOIDWidth_ProbedNeverAssumed ---
	func(t *testing.T) {
		if got := reparentWidth(t, repo); got != 40 {
			t.Fatalf("sha1 repository width = %d, want 40", got)
		}

		outside := canonicalize(t.TempDir())
		_, err := reparentOIDWidth(outside)
		reparentRefusalOf(t, err, ReparentRefusalCapabilityUnsupported)
	}(t)

	// --- TestReparentRefCandidates_ExactlyFiveInOrder ---
	func(t *testing.T) {
		got := reparentRefCandidates("topic")
		want := [5]string{
			"refs/topic",
			"refs/tags/topic",
			"refs/heads/topic",
			"refs/remotes/topic",
			"refs/remotes/topic/HEAD",
		}
		if got != want {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}(t)

	// --- TestReparentIsAncestor_Trichotomy ---
	func(t *testing.T) {
		head := repo.RevParse("HEAD")
		parent := repo.RevParse("HEAD~1")

		if ok, err := reparentIsAncestor(repo.Dir, parent, head); err != nil || !ok {
			t.Fatalf("ancestor case = (%v, %v), want (true, nil)", ok, err)
		}
		if ok, err := reparentIsAncestor(repo.Dir, head, parent); err != nil || ok {
			t.Fatalf("non-ancestor case = (%v, %v), want (false, nil)", ok, err)
		}
		if _, err := reparentIsAncestor(repo.Dir, "refs/heads/does-not-exist", head); err == nil {
			t.Fatal("an unresolvable operand must be an error, never a normal answer")
		}
	}(t)

	// --- TestResolveReparentDestination_EmitsNoDashCArgv ---
	func(t *testing.T) {
		repo.Branch("topic", "HEAD")
		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}

		log := reparentCaptureArgv(t)
		if _, err := ResolveReparentDestination(repo.Dir, stack, GetBranch(stack, "pr2"), "topic", "ref", 40); err != nil {
			t.Fatal(err)
		}
		if len(*log) == 0 {
			t.Fatal("the resolver must emit at least one probe")
		}
		reparentAssertNoDashC(t, *log)
		for _, argv := range *log {
			if argv[0] == "merge-base" {
				found := false
				for _, arg := range argv {
					if arg == "--is-ancestor" {
						found = true
					}
				}
				if !found {
					t.Fatalf("merge-base may only appear as --is-ancestor: %v", argv)
				}
			}
		}
	}(t)
}

// ---------------------------------------------------------------------------
// Destination resolution — kinds, canonical stored form
// ---------------------------------------------------------------------------

func reparentEntryStack() Stack {
	return Stack{Branches: []StackEntry{
		{Name: "pr1", Branch: "feature/pr1", Base: "main"},
		{Name: "pr2", Base: "pr1"},
		{Name: "archived", Branch: "feature/archived", Base: "main", Archived: true},
		{Name: "foreign", Branch: "feature/foreign", Base: "main", Repo: "/elsewhere"},
	}}
}

// T-015, T-016, T-018: --onto-kind auto/entry/ref and the canonical stored forms (AC-011, AC-012, AC-013, AC-018, AC-020).
//
// §17.3 counts one t.Run leaf per real-Git cell, so the assertions below are
// grouped into a single leaf. Each original test keeps its own scope, its own
// repository and its own assertions verbatim; only the leaf boundary moved.
func TestResolveReparentDestination_EntryAndRefKinds(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-015", "onto-kind-matrix")
	assertReparentMatrixBehavior(t, "T-016", "canonical-stored-literal")
	assertReparentMatrixBehavior(t, "T-018", "literal-root")
	assertReparentMatrixBehavior(t, "T-020", "default-branch-token-preserved")
	repo := newReparentPrimitiveRepo(t)
	// --- TestResolveReparentDestination_EntryKindStoresLogicalName ---
	func(t *testing.T) {
		repo.Branch("feature/pr1", "HEAD~1")
		stack := reparentEntryStack()
		target := GetBranch(stack, "pr2")

		for _, kind := range []string{"auto", "entry"} {
			dest, err := ResolveReparentDestination(repo.Dir, stack, target, "pr1", kind, reparentWidth(t, repo))
			if err != nil {
				t.Fatalf("kind %s: %v", kind, err)
			}
			if dest.Kind != ReparentParentKindStackEntry || dest.Resolution != ReparentResolutionEntry {
				t.Fatalf("kind %s: kind=%s resolution=%s", kind, dest.Kind, dest.Resolution)
			}
			if dest.StoredToken != "pr1" {
				t.Fatalf("kind %s: stored token = %q, want the logical entry name", kind, dest.StoredToken)
			}
			if dest.Ref == nil || *dest.Ref != "refs/heads/feature/pr1" {
				t.Fatalf("kind %s: ref = %v, want refs/heads/feature/pr1 (stackBaseRef's spelling)", kind, dest.Ref)
			}
			if dest.SHA != repo.RevParse("HEAD~1") {
				t.Fatalf("kind %s: sha = %s, want the parent branch tip", kind, dest.SHA)
			}
			if dest.EntryName != "pr1" {
				t.Fatalf("kind %s: entry name = %q", kind, dest.EntryName)
			}
		}
	}(t)

	// --- TestResolveReparentDestination_EntryKindRefusals ---
	func(t *testing.T) {
		repo.Branch("feature/archived", "HEAD")
		repo.Branch("feature/foreign", "HEAD")
		repo.Git("branch", "-D", "feature/pr1")
		stack := reparentEntryStack()
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)

		_, err := ResolveReparentDestination(repo.Dir, stack, target, "not-an-entry", "entry", width)
		reparentRefusalOf(t, err, ReparentRefusalDestinationKindMismatch)

		_, err = ResolveReparentDestination(repo.Dir, stack, target, "archived", "entry", width)
		reparentRefusalOf(t, err, ReparentRefusalAffectedArchived)

		_, err = ResolveReparentDestination(repo.Dir, stack, target, "foreign", "entry", width)
		reparentRefusalOf(t, err, ReparentRefusalCrossRepoClosure)

		// An entry whose branch does not exist in Git.
		_, err = ResolveReparentDestination(repo.Dir, stack, target, "pr1", "entry", width)
		reparentRefusalOf(t, err, ReparentRefusalDestinationUnresolvable)

		_, err = ResolveReparentDestination(repo.Dir, stack, target, "   ", "auto", width)
		reparentRefusalOf(t, err, ReparentRefusalDestinationUnset)
	}(t)

	// --- TestResolveReparentDestination_RefKindNeverConsultsEntryNames ---
	func(t *testing.T) {
		// A branch literally named like the stack entry, pointing somewhere else.
		repo.Branch("pr1", "HEAD~2")
		repo.Branch("feature/pr1", "HEAD~1")
		stack := reparentEntryStack()
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)

		auto, err := ResolveReparentDestination(repo.Dir, stack, target, "pr1", "auto", width)
		if err != nil {
			t.Fatal(err)
		}
		if auto.StoredToken != "pr1" || auto.Kind != ReparentParentKindStackEntry {
			t.Fatalf("auto must be entry-first: %+v", auto)
		}

		ref, err := ResolveReparentDestination(repo.Dir, stack, target, "pr1", "ref", width)
		if err != nil {
			t.Fatal(err)
		}
		if ref.Kind != ReparentParentKindLiteralRef || ref.StoredToken != "refs/heads/pr1" {
			t.Fatalf("--onto-kind ref must resolve the branch and store it canonically: %+v", ref)
		}
		if ref.SHA != repo.RevParse("HEAD~2") {
			t.Fatalf("ref destination sha = %s, want the branch named pr1", ref.SHA)
		}
	}(t)

	// --- TestResolveReparentDestination_CanonicalStoredForms ---
	func(t *testing.T) {
		head := repo.RevParse("HEAD")
		repo.Branch("topic", "HEAD")
		repo.Git("tag", "v1", "HEAD~1")
		repo.Git("update-ref", "refs/remotes/origin/main", head)
		repo.Git("update-ref", "refs/custom/thing", head)
		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)

		cases := []struct {
			token      string
			stored     string
			resolution string
			ref        string
			sha        string
		}{
			{"topic", "refs/heads/topic", ReparentResolutionRefHead, "refs/heads/topic", head},
			{"v1", "refs/tags/v1", ReparentResolutionRefTag, "refs/tags/v1", repo.RevParse("HEAD~1")},
			{"origin/main", "refs/remotes/origin/main", ReparentResolutionRefRemote, "refs/remotes/origin/main", head},
			{"refs/custom/thing", "refs/custom/thing", ReparentResolutionRefFull, "refs/custom/thing", head},
			{"refs/heads/topic", "refs/heads/topic", ReparentResolutionRefFull, "refs/heads/topic", head},
			{head, head, ReparentResolutionRawOID, "", head},
			{strings.ToUpper(head), head, ReparentResolutionRawOID, "", head},
		}

		for _, tc := range cases {
			t.Run(tc.token, func(t *testing.T) {
				dest, err := ResolveReparentDestination(repo.Dir, stack, target, tc.token, "ref", width)
				if err != nil {
					t.Fatalf("%q: %v", tc.token, err)
				}
				if dest.StoredToken != tc.stored {
					t.Fatalf("stored token = %q, want %q", dest.StoredToken, tc.stored)
				}
				if dest.Resolution != tc.resolution {
					t.Fatalf("resolution = %q, want %q", dest.Resolution, tc.resolution)
				}
				if tc.ref == "" {
					if dest.Ref != nil {
						t.Fatalf("ref = %v, want null for an object-id destination", *dest.Ref)
					}
				} else if dest.Ref == nil || *dest.Ref != tc.ref {
					t.Fatalf("ref = %v, want %q", dest.Ref, tc.ref)
				}
				if dest.SHA != tc.sha {
					t.Fatalf("sha = %s, want %s", dest.SHA, tc.sha)
				}
				if dest.RequestedToken != tc.token {
					t.Fatalf("requested token = %q, want the operator's own spelling %q", dest.RequestedToken, tc.token)
				}
			})
		}
	}(t)

	// --- TestResolveReparentDestination_AnnotatedTagPeelsToCommit ---
	func(t *testing.T) {
		repo.Git("tag", "-a", "rel1", "-m", "annotated", "HEAD")
		tagObject := repo.RevParse("refs/tags/rel1")
		commit := repo.RevParse("refs/tags/rel1^{commit}")
		if tagObject == commit {
			t.Fatal("fixture must produce a real annotated tag object")
		}

		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		dest, err := ResolveReparentDestination(repo.Dir, stack, GetBranch(stack, "pr2"), "rel1", "ref", reparentWidth(t, repo))
		if err != nil {
			t.Fatal(err)
		}
		if dest.SHA != commit {
			t.Fatalf("sha = %s, want the peeled commit %s (not the tag object %s)", dest.SHA, commit, tagObject)
		}
		if dest.StoredToken != "refs/tags/rel1" {
			t.Fatalf("stored token = %q", dest.StoredToken)
		}
	}(t)
}

// T-011, T-012, T-014, T-019: pseudo refs, ambiguity, the object-id rungs and their partial facts (AC-014, AC-015, AC-017, AC-019).
//
// §17.3 counts one t.Run leaf per real-Git cell, so the assertions below are
// grouped into a single leaf. Each original test keeps its own scope, its own
// repository and its own assertions verbatim; only the leaf boundary moved.
func TestResolveReparentDestination_RefusalRungs(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-011", "destination-ambiguous-short-ref")
	assertReparentMatrixBehavior(t, "T-012", "oid-disambiguation")
	assertReparentMatrixBehavior(t, "T-014", "pseudo-ref-rejected")
	repo := newReparentPrimitiveRepo(t)
	// --- TestResolveReparentDestination_PseudoRefsRefuse ---
	func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)

		for _, token := range []string{
			"HEAD", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD",
			"REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE", "BISECT_HEAD",
		} {
			_, err := ResolveReparentDestination(repo.Dir, stack, target, token, "ref", width)
			detail := reparentRefusalOf(t, err, ReparentRefusalDestinationUnresolvable)
			if detail != reparentPseudoRefDetail {
				t.Fatalf("%s detail = %q, want the exact pseudo-ref sentence", token, detail)
			}
		}

		// An ordinary uppercase token is NOT a pseudo ref and still resolves.
		repo.Git("branch", "RELEASE")
		dest, err := ResolveReparentDestination(repo.Dir, stack, target, "RELEASE", "ref", width)
		if err != nil {
			t.Fatalf("an ordinary uppercase short name must remain resolvable: %v", err)
		}
		if dest.StoredToken != "refs/heads/RELEASE" {
			t.Fatalf("stored token = %q", dest.StoredToken)
		}
	}(t)

	// --- TestResolveReparentDestination_AmbiguityIsRefusedNotGuessed ---
	func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)

		// A branch and a tag with the same short name.
		repo.Branch("dual", "HEAD")
		repo.Git("tag", "dual", "HEAD~1")
		dual, err := ResolveReparentDestination(repo.Dir, stack, target, "dual", "ref", width)
		detail := reparentRefusalOf(t, err, ReparentRefusalDestinationAmbiguous)
		reparentAssertPublishedResolution(t, dual, "dual", ReparentResolutionAmbiguous,
			[]string{"refs/heads/dual", "refs/tags/dual"})
		for _, want := range []string{"refs/heads/dual", "refs/tags/dual", "full ref"} {
			if !strings.Contains(detail, want) {
				t.Fatalf("detail %q must name %q", detail, want)
			}
		}

		// for-each-ref matches patterns up to a slash, so refs/heads/nested/one
		// is returned for the pattern refs/heads/nested. The exact-equality
		// filter is what stops it from counting as a candidate.
		repo.Branch("nested/one", "HEAD")
		nested, err := ResolveReparentDestination(repo.Dir, stack, target, "nested", "ref", width)
		reparentRefusalOf(t, err, ReparentRefusalDestinationUnresolvable)
		reparentAssertPublishedResolution(t, nested, "nested", ReparentResolutionUnresolved, nil)
	}(t)

	// --- TestResolveReparentDestination_ObjectIDRungs ---
	func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)
		head := repo.RevParse("HEAD")

		// A unique abbreviation resolves to the full canonical OID.
		dest, err := ResolveReparentDestination(repo.Dir, stack, target, head[:10], "ref", width)
		if err != nil {
			t.Fatalf("unique abbreviation: %v", err)
		}
		if dest.StoredToken != head || dest.Resolution != ReparentResolutionRawOID {
			t.Fatalf("abbreviated OID stored %q (%s), want the full %q", dest.StoredToken, dest.Resolution, head)
		}

		// A hex token that is no object at all falls through to the short-name
		// rung, where a branch of that name resolves normally.
		repo.Branch("deadbeef", "HEAD~1")
		fallthroughDest, err := ResolveReparentDestination(repo.Dir, stack, target, "deadbeef", "ref", width)
		if err != nil {
			t.Fatalf("hex-shaped branch name: %v", err)
		}
		if fallthroughDest.StoredToken != "refs/heads/deadbeef" {
			t.Fatalf("stored token = %q, want the branch", fallthroughDest.StoredToken)
		}

		// A token that is BOTH an object prefix and a ref name is ambiguous.
		bothName := head[:8]
		repo.Branch(bothName, "HEAD~2")
		both, err := ResolveReparentDestination(repo.Dir, stack, target, bothName, "ref", width)
		detail := reparentRefusalOf(t, err, ReparentRefusalDestinationAmbiguous)
		if !strings.Contains(detail, "refs/heads/"+bothName) {
			t.Fatalf("detail %q must name the colliding ref", detail)
		}
		reparentAssertPublishedResolution(t, both, bothName, ReparentResolutionAmbiguous,
			[]string{"refs/heads/" + bothName})
		if !strings.Contains(detail, head) {
			t.Fatalf("detail %q must also name the object the token matched", detail)
		}
	}(t)

	// --- TestResolveReparentDestination_AbbreviatedOIDCollisionRefuses ---
	func(t *testing.T) {
		oids := reparentBulkObjects(t, repo, 1200)
		prefix := reparentCollidingPrefix(t, oids)

		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		dest, err := ResolveReparentDestination(repo.Dir, stack, GetBranch(stack, "pr2"), prefix, "ref", reparentWidth(t, repo))
		detail := reparentRefusalOf(t, err, ReparentRefusalDestinationAmbiguous)
		if !strings.Contains(detail, "objects") {
			t.Fatalf("detail %q must report the multi-object match", detail)
		}

		// The rung's partial facts must survive the refusal: an unavailable plan
		// publishes WHAT was ambiguous, not an empty cell.
		var matching []string
		for _, oid := range oids {
			if strings.HasPrefix(oid, prefix) {
				matching = append(matching, oid)
			}
		}
		sort.Strings(matching)
		if len(matching) < 2 {
			t.Fatalf("fixture produced %d objects for prefix %q, want at least 2", len(matching), prefix)
		}
		reparentAssertPublishedResolution(t, dest, prefix, ReparentResolutionAmbiguous, matching)
		for _, candidate := range dest.Candidates {
			if len(candidate) != reparentWidth(t, repo) {
				t.Fatalf("candidate %q is not a full object id", candidate)
			}
			if !strings.Contains(detail, candidate) {
				t.Fatalf("detail %q must list candidate %q", detail, candidate)
			}
		}
	}(t)

	// --- TestResolveReparentDestination_NonCommittishObjectPublishesUnresolved ---
	func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{{Name: "pr2", Base: "main"}}}
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)

		blobPath := filepath.Join(repo.Dir, "blob-only.txt")
		if err := os.WriteFile(blobPath, []byte("a blob, never a commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		blob := strings.ToLower(repo.Git("hash-object", "-w", blobPath))
		if len(blob) != width {
			t.Fatalf("blob id %q is not %d characters", blob, width)
		}

		cases := []struct {
			name  string
			token string
		}{
			{"full object id of a blob", blob},
			{"unique abbreviation of a blob", blob[:12]},
			{"full object id of nothing", strings.Repeat("0", width)},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				dest, err := ResolveReparentDestination(repo.Dir, stack, target, tc.token, "ref", width)
				reparentRefusalOf(t, err, ReparentRefusalDestinationUnresolvable)
				reparentAssertPublishedResolution(t, dest, tc.token, ReparentResolutionUnresolved, nil)
			})
		}

		// A cutoff token sees the identical rungs under its own kind.
		_, err := resolveReparentTargetCutoff(repo.Dir, stack, target, "", blob, width)
		reparentRefusalOf(t, err, ReparentRefusalCutoffUnresolvable)
	}(t)

	// --- TestResolveReparentLiteral_ForwardsPartialFactsFromTheObjectRung ---
	func(t *testing.T) {
		width := reparentWidth(t, repo)

		blobPath := filepath.Join(repo.Dir, "literal-blob.txt")
		if err := os.WriteFile(blobPath, []byte("blob, not a commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		blob := strings.ToLower(repo.Git("hash-object", "-w", blobPath))

		oids := reparentBulkObjects(t, repo, 1200)
		prefix := reparentCollidingPrefix(t, oids)
		var matching []string
		for _, oid := range oids {
			if strings.HasPrefix(oid, prefix) {
				matching = append(matching, oid)
			}
		}
		sort.Strings(matching)

		t.Run("multi-match abbreviation", func(t *testing.T) {
			literal, err := resolveReparentLiteral(repo.Dir, prefix, width,
				ReparentRefusalDestinationUnresolvable, ReparentRefusalDestinationAmbiguous)
			reparentRefusalOf(t, err, ReparentRefusalDestinationAmbiguous)
			if literal.Resolution != ReparentResolutionAmbiguous {
				t.Fatalf("resolution = %q, want %q", literal.Resolution, ReparentResolutionAmbiguous)
			}
			if len(literal.Candidates) != len(matching) {
				t.Fatalf("candidates = %v, want the %d matching object ids", literal.Candidates, len(matching))
			}
			for i := range matching {
				if literal.Candidates[i] != matching[i] {
					t.Fatalf("candidate %d = %q, want %q (sorted)", i, literal.Candidates[i], matching[i])
				}
			}
			if literal.SHA != "" || literal.StoredToken != "" || literal.Ref != nil {
				t.Fatalf("an ambiguous literal must pin nothing: %+v", literal)
			}
		})

		t.Run("non-committish object", func(t *testing.T) {
			for _, token := range []string{blob, blob[:12]} {
				literal, err := resolveReparentLiteral(repo.Dir, token, width,
					ReparentRefusalDestinationUnresolvable, ReparentRefusalDestinationAmbiguous)
				reparentRefusalOf(t, err, ReparentRefusalDestinationUnresolvable)
				if literal.Resolution != ReparentResolutionUnresolved {
					t.Fatalf("%q resolution = %q, want %q", token, literal.Resolution, ReparentResolutionUnresolved)
				}
				if len(literal.Candidates) != 0 {
					t.Fatalf("%q candidates = %v, want none (nothing was ambiguous)", token, literal.Candidates)
				}
			}
		})

		t.Run("cutoff kind mapping keeps the same partial facts", func(t *testing.T) {
			literal, err := resolveReparentLiteral(repo.Dir, prefix, width,
				ReparentRefusalCutoffUnresolvable, ReparentRefusalCutoffUnresolvable)
			reparentRefusalOf(t, err, ReparentRefusalCutoffUnresolvable)
			if literal.Resolution != ReparentResolutionAmbiguous || len(literal.Candidates) != len(matching) {
				t.Fatalf("a cutoff ambiguity must publish the same facts under its own kind: %+v", literal)
			}
		})
	}(t)
}

// TestResolveReparentDestination_NonCommittishObjectPublishesUnresolved covers
// the other object-id refusal arms: a full OID and a unique abbreviation that
// both name a real non-commit object, and a full OID that names nothing.
// Each must publish resolution "unresolved" with no candidates, because
// nothing was ambiguous.

// ---------------------------------------------------------------------------
// Cutoff ladders
// ---------------------------------------------------------------------------

// T-004, T-005, T-006, T-007, T-008: both cutoff ladders end to end (AC-022 through AC-030).
//
// §17.3 counts one t.Run leaf per real-Git cell, so the assertions below are
// grouped into a single leaf. Each original test keeps its own scope, its own
// repository and its own assertions verbatim; only the leaf boundary moved.
func TestResolveReparentCutoffLadders(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-004", "cutoff-recorded-conflict-unresolvable")
	assertReparentMatrixBehavior(t, "T-005", "cutoff-supplied-old-parent-absent")
	assertReparentMatrixBehavior(t, "T-006", "cutoff-nonancestor-refusal")
	assertReparentMatrixBehavior(t, "T-007", "descendant-cutoff-snapshot")
	assertReparentMatrixBehavior(t, "T-008", "cutoff-ambiguity-one-kind")
	_ = "asserts AC-022 AC-023 AC-024 AC-025 AC-026 AC-027 AC-028 AC-029 AC-030"
	repo := newReparentPrimitiveRepo(t)
	// --- TestResolveReparentTargetCutoff_Ladder ---
	func(t *testing.T) {
		repo.Git("branch", "feature/pr1", "HEAD~1")
		repo.Git("branch", "feature/pr2", "HEAD")
		stack := Stack{Branches: []StackEntry{
			{Name: "pr1", Branch: "feature/pr1", Base: "main"},
			{Name: "pr2", Branch: "feature/pr2", Base: "pr1"},
		}}
		target := GetBranch(stack, "pr2")
		width := reparentWidth(t, repo)
		recorded := repo.RevParse("HEAD~2")
		parentTip := repo.RevParse("refs/heads/feature/pr1")

		t.Run("recorded wins", func(t *testing.T) {
			cutoff, err := resolveReparentTargetCutoff(repo.Dir, stack, target, recorded, "", width)
			if err != nil {
				t.Fatal(err)
			}
			if cutoff.Provenance != ReparentCutoffRecordedBySync || cutoff.RecordedState != ReparentCutoffRecordPresent {
				t.Fatalf("cutoff = %+v", cutoff)
			}
			if cutoff.ResolvedSHA == nil || *cutoff.ResolvedSHA != recorded {
				t.Fatalf("resolved = %v, want %s", cutoff.ResolvedSHA, recorded)
			}
		})

		t.Run("supplied agreeing with recorded", func(t *testing.T) {
			cutoff, err := resolveReparentTargetCutoff(repo.Dir, stack, target, recorded, recorded, width)
			if err != nil {
				t.Fatal(err)
			}
			if cutoff.SuppliedSHA == nil || *cutoff.SuppliedSHA != recorded {
				t.Fatalf("supplied sha = %v", cutoff.SuppliedSHA)
			}
			if cutoff.Provenance != ReparentCutoffRecordedBySync {
				t.Fatalf("provenance = %s, want the recorded boundary to stay authoritative", cutoff.Provenance)
			}
		})

		t.Run("supplied disagreeing refuses cutoff-conflict", func(t *testing.T) {
			cutoff, err := resolveReparentTargetCutoff(repo.Dir, stack, target, recorded, parentTip, width)
			reparentRefusalOf(t, err, ReparentRefusalCutoffConflict)
			if cutoff.Conflict == nil || !strings.Contains(*cutoff.Conflict, recorded) || !strings.Contains(*cutoff.Conflict, parentTip) {
				t.Fatalf("conflict cell = %v, want both SHAs named", cutoff.Conflict)
			}
		})

		t.Run("recorded but unresolvable refuses", func(t *testing.T) {
			missing := strings.Repeat("0", 40)
			cutoff, err := resolveReparentTargetCutoff(repo.Dir, stack, target, missing, parentTip, width)
			reparentRefusalOf(t, err, ReparentRefusalCutoffUnresolvable)
			if cutoff.RecordedState != ReparentCutoffRecordUnresolvable {
				t.Fatalf("recorded state = %s", cutoff.RecordedState)
			}
		})

		t.Run("operator supplied when nothing is recorded", func(t *testing.T) {
			cutoff, err := resolveReparentTargetCutoff(repo.Dir, stack, target, "", "feature/pr1", width)
			if err != nil {
				t.Fatal(err)
			}
			if cutoff.Provenance != ReparentCutoffOperatorSupplied {
				t.Fatalf("provenance = %s", cutoff.Provenance)
			}
			if cutoff.ResolvedSHA == nil || *cutoff.ResolvedSHA != parentTip {
				t.Fatalf("resolved = %v, want %s", cutoff.ResolvedSHA, parentTip)
			}
		})

		t.Run("old parent tip when it is an ancestor", func(t *testing.T) {
			cutoff, err := resolveReparentTargetCutoff(repo.Dir, stack, target, "", "", width)
			if err != nil {
				t.Fatal(err)
			}
			if cutoff.Provenance != ReparentCutoffOldParentTip {
				t.Fatalf("provenance = %s", cutoff.Provenance)
			}
			if cutoff.AncestorOfBranch == nil || !*cutoff.AncestorOfBranch {
				t.Fatalf("ancestor cell = %v, want true", cutoff.AncestorOfBranch)
			}
		})

		t.Run("no rung produces a boundary", func(t *testing.T) {
			repo.Git("branch", "unrelated-root", "HEAD")
			divergent := Stack{Branches: []StackEntry{
				{Name: "orphan", Branch: "unrelated-root", Base: ""},
			}}
			_, err := resolveReparentTargetCutoff(repo.Dir, divergent, GetBranch(divergent, "orphan"), "", "", width)
			detail := reparentRefusalOf(t, err, ReparentRefusalCutoffAbsent)
			for _, want := range []string{"--cutoff", "tws sync"} {
				if !strings.Contains(detail, want) {
					t.Fatalf("detail %q must name both remedies", detail)
				}
			}
		})

		t.Run("pseudo ref and ambiguity both refuse cutoff-unresolvable", func(t *testing.T) {
			_, err := resolveReparentTargetCutoff(repo.Dir, stack, target, "", "HEAD", width)
			detail := reparentRefusalOf(t, err, ReparentRefusalCutoffUnresolvable)
			if detail != reparentPseudoRefDetail {
				t.Fatalf("detail = %q", detail)
			}

			repo.Branch("dual-cutoff", "HEAD")
			repo.Git("tag", "dual-cutoff", "HEAD~1")
			_, err = resolveReparentTargetCutoff(repo.Dir, stack, target, "", "dual-cutoff", width)
			ambiguousDetail := reparentRefusalOf(t, err, ReparentRefusalCutoffUnresolvable)
			if !strings.Contains(ambiguousDetail, "refs/tags/dual-cutoff") {
				t.Fatalf("an ambiguous cutoff must list its candidates: %q", ambiguousDetail)
			}
		})
	}(t)

	// --- TestResolveReparentDescendantCutoff_Ladder ---
	func(t *testing.T) {
		repo.Git("branch", "-f", "feature/pr2", "HEAD~1")
		repo.Git("branch", "feature/pr3", "HEAD")
		stack := Stack{Branches: []StackEntry{
			{Name: "pr2", Branch: "feature/pr2", Base: "main"},
			{Name: "pr3", Branch: "feature/pr3", Base: "pr2"},
		}}
		row := GetBranch(stack, "pr3")
		width := reparentWidth(t, repo)
		recorded := repo.RevParse("HEAD~2")

		cutoff, err := resolveReparentDescendantCutoff(repo.Dir, stack, row, recorded, width)
		if err != nil {
			t.Fatal(err)
		}
		if cutoff.Provenance != ReparentCutoffRecordedBySync || cutoff.ResolvedSHA == nil || *cutoff.ResolvedSHA != recorded {
			t.Fatalf("recorded rung = %+v", cutoff)
		}

		cutoff, err = resolveReparentDescendantCutoff(repo.Dir, stack, row, "", width)
		if err != nil {
			t.Fatal(err)
		}
		if cutoff.Provenance != ReparentCutoffOldParentTip {
			t.Fatalf("parent-tip rung = %+v", cutoff)
		}
		if cutoff.ResolvedSHA == nil || *cutoff.ResolvedSHA != repo.RevParse("refs/heads/feature/pr2") {
			t.Fatalf("resolved = %v, want the configured parent's tip", cutoff.ResolvedSHA)
		}

		// A parent tip that is not an ancestor of the row's branch produces no
		// boundary at all: the descendant refuses its own kind, never the
		// target's.
		repo.Git("checkout", "-q", "-b", "feature/detached", "HEAD~2")
		detached := Stack{Branches: []StackEntry{
			{Name: "pr2", Branch: "feature/pr2", Base: "main"},
			{Name: "pr9", Branch: "feature/detached", Base: "pr2"},
		}}
		_, err = resolveReparentDescendantCutoff(repo.Dir, detached, GetBranch(detached, "pr9"), "", width)
		detail := reparentRefusalOf(t, err, ReparentRefusalDescendantCutoffAbsent)
		if !strings.Contains(detail, "--from") {
			t.Fatalf("detail %q must name the descendant remedy", detail)
		}

		unresolvable, err := resolveReparentDescendantCutoff(repo.Dir, stack, row, strings.Repeat("0", 40), width)
		reparentRefusalOf(t, err, ReparentRefusalCutoffUnresolvable)
		if unresolvable.RecordedState != ReparentCutoffRecordUnresolvable {
			t.Fatalf("recorded state = %s", unresolvable.RecordedState)
		}
	}(t)
}

// ---------------------------------------------------------------------------
// Resolver agreement (§4.3c)
// ---------------------------------------------------------------------------

// T-017, T-020, T-021: the four-resolver agreement, its annotated-tag normalization and an injected divergence (AC-021).
//
// §17.3 counts one t.Run leaf per real-Git cell, so the assertions below are
// grouped into a single leaf. Each original test keeps its own scope, its own
// repository and its own assertions verbatim; only the leaf boundary moved.
func TestResolveReparentAgreement_FourResolvers(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-017", "resolver-agreement")
	repo := newReparentPrimitiveRepo(t)
	// --- TestResolveReparentAgreement_AllFourResolversAgreeForAnEntryDestination ---
	func(t *testing.T) {
		repo.Git("branch", "feature/pr1", "HEAD~1")
		repo.Git("branch", "feature/pr2", "HEAD")
		postImage := Stack{Branches: []StackEntry{
			{Name: "pr1", Branch: "feature/pr1", Base: "main"},
			{Name: "pr2", Branch: "feature/pr2", Base: "pr1"},
		}}
		target := GetBranch(postImage, "pr2")
		pinned := repo.RevParse("refs/heads/feature/pr1")

		agreement, err := resolveReparentAgreement(repo.Dir, postImage, target, ReparentParentKindStackEntry, "pr1", pinned)
		if err != nil {
			t.Fatalf("agreement: %v", err)
		}
		if !agreement.Checked || agreement.Agreed == nil || !*agreement.Agreed {
			t.Fatalf("agreement = %+v", agreement)
		}
		wantNames := []string{
			ReparentResolverDestination, ReparentResolverSyncBase,
			ReparentResolverStackBase, ReparentResolverCheckout,
		}
		if len(agreement.Resolvers) != len(wantNames) {
			t.Fatalf("%d verdicts, want %d", len(agreement.Resolvers), len(wantNames))
		}
		for i, verdict := range agreement.Resolvers {
			if verdict.Name != wantNames[i] {
				t.Fatalf("verdict %d = %s, want %s (§4.3c order)", i, verdict.Name, wantNames[i])
			}
			if !verdict.Agrees {
				t.Fatalf("verdict %s disagreed: %v", verdict.Name, verdict.Detail)
			}
			if verdict.SHA == nil || *verdict.SHA != pinned {
				t.Fatalf("verdict %s sha = %v, want %s", verdict.Name, verdict.SHA, pinned)
			}
		}
	}(t)

	t.Run("literal stored token never collides with entry name", func(t *testing.T) {
		main := repo.RevParse("main")
		repo.Branch("semantic-entry", main)
		postImage := Stack{Branches: []StackEntry{
			{Name: "refs/heads/main", Branch: "semantic-entry", Base: "main"},
			{Name: "target", Branch: "topic", Base: "refs/heads/main"},
		}}
		repo.Branch("topic", main)
		target := GetBranch(postImage, "target")

		_, err := resolveReparentAgreement(repo.Dir, postImage, target,
			ReparentParentKindLiteralRef, "refs/heads/main", main)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) ||
			refusal.Kind != ReparentRefusalDestinationResolverDivergent ||
			!strings.Contains(refusal.Detail, "also the logical stack entry name") {
			t.Fatalf("equal-tip semantic collision = %v", err)
		}

		repo.Git("switch", "-q", "semantic-entry")
		repo.Commit("semantic-move.txt", "move")
		repo.Git("switch", "-q", "main")
		_, err = resolveReparentAgreement(repo.Dir, postImage, target,
			ReparentParentKindLiteralRef, "refs/heads/main", main)
		if !asReparentRefusal(err, &refusal) ||
			refusal.Kind != ReparentRefusalDestinationResolverDivergent {
			t.Fatalf("moved-entry semantic collision = %v", err)
		}

		postImage.Branches = append(postImage.Branches,
			StackEntry{Name: main, Branch: "semantic-oid", Base: "main"})
		repo.Branch("semantic-oid", main)
		target.Base = main
		_, err = resolveReparentAgreement(repo.Dir, postImage, target,
			ReparentParentKindLiteralRef, main, main)
		if !asReparentRefusal(err, &refusal) ||
			refusal.Kind != ReparentRefusalDestinationResolverDivergent {
			t.Fatalf("raw-OID semantic collision = %v", err)
		}
	})

	// --- TestResolveReparentAgreement_AnnotatedTagNormalizesTheCheckoutResolver ---
	func(t *testing.T) {
		repo.Git("branch", "-f", "feature/pr2", "HEAD")
		repo.Git("tag", "-a", "rel1", "-m", "annotated", "HEAD~1")
		tagObject := repo.RevParse("refs/tags/rel1")
		commit := repo.RevParse("refs/tags/rel1^{commit}")
		if tagObject == commit {
			t.Fatal("fixture must produce a real annotated tag object")
		}

		postImage := Stack{Branches: []StackEntry{
			{Name: "pr2", Branch: "feature/pr2", Base: "refs/tags/rel1"},
		}}
		target := GetBranch(postImage, "pr2")

		agreement, err := resolveReparentAgreement(repo.Dir, postImage, target, ReparentParentKindLiteralRef, "refs/tags/rel1", commit)
		if err != nil {
			t.Fatalf("annotated-tag agreement: %v", err)
		}
		if agreement.Agreed == nil || !*agreement.Agreed {
			t.Fatalf("agreement = %+v", agreement)
		}
		checkout := agreement.Resolvers[len(agreement.Resolvers)-1]
		if checkout.Name != ReparentResolverCheckout {
			t.Fatalf("last verdict = %s", checkout.Name)
		}
		if checkout.SHA == nil || *checkout.SHA != commit {
			t.Fatalf("checkout verdict sha = %v, want the PEELED commit %s (the bare rev-parse observes %s)",
				checkout.SHA, commit, tagObject)
		}
	}(t)

	// --- TestResolveReparentAgreement_DivergenceRefusesAndNamesEverything ---
	func(t *testing.T) {
		repo.Git("branch", "-f", "feature/pr1", "HEAD~1")
		repo.Git("branch", "-f", "feature/pr2", "HEAD")
		postImage := Stack{Branches: []StackEntry{
			{Name: "pr1", Branch: "feature/pr1", Base: "main"},
			{Name: "pr2", Branch: "feature/pr2", Base: "pr1"},
		}}
		target := GetBranch(postImage, "pr2")
		// A pinned value the other three resolvers cannot possibly produce.
		wrong := repo.RevParse("HEAD~2")

		agreement, err := resolveReparentAgreement(repo.Dir, postImage, target, ReparentParentKindStackEntry, "pr1", wrong)
		detail := reparentRefusalOf(t, err, ReparentRefusalDestinationResolverDivergent)
		if agreement.Agreed == nil || *agreement.Agreed {
			t.Fatalf("agreement must publish the disagreement: %+v", agreement)
		}
		for _, want := range []string{"pr1", wrong, repo.RevParse("refs/heads/feature/pr1")} {
			if !strings.Contains(detail, want) {
				t.Fatalf("detail %q must name %q", detail, want)
			}
		}
	}(t)
}

// TestResolveReparentLiteral_ForwardsPartialFactsFromTheObjectRung locks the
// fix at its source rather than only at the destination boundary: the object
// rung's own literal — ambiguous with the sorted candidate OIDs, or
// unresolved for a non-committish object — must reach the caller instead of
// being replaced by a zero value whose resolution is outside the closed
// domain.
