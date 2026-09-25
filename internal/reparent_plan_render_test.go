package internal

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2).
//
//	T-032 deferred descendant facts: nulls, runnable, the literal . AC-046
// ---------------------------------------------------------------------------

// TestMarshalReparentPlan_OneCompactValueOneNewline pins the wire contract:
// exactly one JSON value, exactly one trailing newline, no indentation, and
// no HTML escaping.
func TestMarshalReparentPlan_OneCompactValueOneNewline(t *testing.T) {
	plan := reparentSamplePlan()
	detail := "a <script> & \"quoted\" detail"
	plan.Blockers = []ReparentPlanBlocker{{Kind: ReparentRefusalNoWork, Detail: detail}}

	out, err := MarshalReparentPlan(plan)
	if err != nil {
		t.Fatalf("MarshalReparentPlan: %v", err)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Fatal("document must end with exactly one newline")
	}
	if strings.Count(string(out), "\n") != 1 {
		t.Fatalf("document contains %d newlines, want exactly 1 (compact)", strings.Count(string(out), "\n"))
	}
	if strings.Contains(string(out), "\\u003c") || strings.Contains(string(out), "\\u0026") {
		t.Fatal("HTML escaping must be disabled")
	}
	if !strings.Contains(string(out), `a <script> & \"quoted\" detail`) {
		t.Fatalf("detail was rewritten beyond ordinary JSON quoting: %s", out)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("second decode = %v, want exactly one value then EOF", err)
	}
}

// TestMarshalReparentPlan_NormalizesNeverNullArrays proves a nil slice never
// reaches the wire as null, and that the shipped nullable-array exception is
// preserved: replay.commits distinguishes "not probed" from "probed, empty".
func TestMarshalReparentPlan_NormalizesNeverNullArrays(t *testing.T) {
	plan := reparentSamplePlan()
	plan.Descendants = nil
	plan.Blockers = nil
	plan.Warnings = nil
	plan.Repositories = nil
	plan.Remote.Rows = nil
	plan.Remote.Guidance = nil
	plan.Holders.Rows = nil
	plan.Summary.CollateralRefs = nil
	plan.Target.Argv = nil
	plan.Target.Notes = nil
	plan.Target.CollateralRefs = nil
	plan.Target.NewParent.Candidates = nil
	plan.Target.NewParent.ResolverAgreement.Resolvers = nil
	plan.Target.Replay.Commits = nil

	out, err := MarshalReparentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, key := range []string{
		`"descendants":[]`, `"blockers":[]`, `"warnings":[]`, `"repositories":[]`,
		`"rows":[]`, `"guidance":[]`, `"collateral_refs":[]`, `"argv":[]`,
		`"notes":[]`, `"candidates":[]`, `"resolvers":[]`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("%s is missing from the normalized document:\n%s", key, text)
		}
	}
	if !strings.Contains(text, `"commits":null`) {
		t.Fatal("replay.commits must keep its nullable-array exception")
	}
}

// TestMarshalReparentPlan_DeferredCellsAreNull proves §7.5a on the wire: a
// value that cannot exist yet is published as an explicit null, never as a
// zero, a blank, or an invented SHA.
func TestMarshalReparentPlan_DeferredCellsAreNull(t *testing.T) {
	out, err := MarshalReparentPlan(reparentSamplePlan())
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	descendants, ok := doc["descendants"].([]any)
	if !ok || len(descendants) != 1 {
		t.Fatalf("descendants = %v", doc["descendants"])
	}
	row := descendants[0].(map[string]any)
	if row["destination_sha"] != nil {
		t.Fatalf("destination_sha = %v, want null for a parent-computed row", row["destination_sha"])
	}
	if newParent := row["new_parent"].(map[string]any); newParent["sha"] != nil {
		t.Fatalf("new_parent.sha = %v, want null", newParent["sha"])
	}
	if row["destination_parent"] != "pr2" {
		t.Fatalf("destination_parent = %v, want the parent row's name", row["destination_parent"])
	}

	delta := doc["metadata_delta"].(map[string]any)
	if delta["stack_sha256_after_expected"] != nil {
		t.Fatalf("stack_sha256_after_expected = %v, want null while a row is deferred", delta["stack_sha256_after_expected"])
	}
	entries := delta["entries"].([]any)
	if entries[1].(map[string]any)["last_base_sha_after"] != nil {
		t.Fatal("a descendant's last_base_sha_after must be null on a plan route")
	}
	if entries[1].(map[string]any)["changed"] != true {
		t.Fatal("a deferred descendant metadata cell must still publish changed=true")
	}

	strategy := doc["strategy"].(map[string]any)
	if strategy["run_id"] != nil || strategy["computation_path"] != nil {
		t.Fatalf("a plan mints no run id and publishes no computation path: %v", strategy)
	}

	// A deferred document is still runnable and carries no refusal.
	if doc["runnable"] != true {
		t.Fatal("deferred nulls must not make a plan unrunnable")
	}
	if refusal := doc["refusal"].(map[string]any); refusal["kind"] != nil {
		t.Fatalf("refusal.kind = %v, want null", refusal["kind"])
	}
}

// TestMarshalReparentPlan_TopLevelKeyOrderOnTheWire proves the document's key
// order is the declaration order the schema freezes.
func TestMarshalReparentPlan_TopLevelKeyOrderOnTheWire(t *testing.T) {
	out, err := MarshalReparentPlan(reparentSamplePlan())
	if err != nil {
		t.Fatal(err)
	}
	want := reparentJSONKeys(t, ReparentPlan{})

	dec := json.NewDecoder(strings.NewReader(string(out)))
	if _, err := dec.Token(); err != nil { // opening brace
		t.Fatal(err)
	}
	var got []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		case string:
			if depth == 0 {
				got = append(got, v)
				var skip any
				if err := dec.Decode(&skip); err != nil {
					t.Fatal(err)
				}
			}
		}
		if depth < 0 {
			break
		}
	}
	if len(got) != len(want) {
		t.Fatalf("wire has %d top-level keys, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wire key %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFormatReparentPlan_SectionsAndDeferredLiteral pins the human document's
// section order and the one literal every deferred cell renders as.
func TestFormatReparentPlan_SectionsAndDeferredLiteral(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-032", "human-sections-and-deferred-literal")
	_ = "asserts AC-046"
	out, err := FormatReparentPlan(reparentSamplePlan())
	if err != nil {
		t.Fatalf("FormatReparentPlan: %v", err)
	}
	text := string(out)

	sections := []string{
		"reparent plan  (schema 1, route fresh)",
		"target",
		"descendants (1)",
		"metadata changes",
		"strategy and atomicity",
		"holders",
		"remote follow-up",
		"guard",
		"approval",
	}
	position := 0
	for _, section := range sections {
		idx := strings.Index(text[position:], section)
		if idx < 0 {
			t.Fatalf("section %q is missing or out of order:\n%s", section, text)
		}
		position += idx
	}

	if !strings.Contains(text, "onto computed tip of pr2") {
		t.Fatalf("a descendant must render its destination dependency:\n%s", text)
	}
	if strings.Count(text, ReparentDeferredRendering) != 2 {
		t.Fatalf("only descendant-derived metadata cells may render as %q:\n%s", ReparentDeferredRendering, text)
	}
	if !strings.Contains(text, "run id: not assigned, computation path: not assigned") {
		t.Fatalf("plan-only execution identity must be explicitly unassigned:\n%s", text)
	}
	if strings.Contains(text, "0000000000000000") {
		t.Fatal("a deferred cell must never render as a zero SHA")
	}
	if !strings.Contains(text, "resolver agreement: agreed=yes") {
		t.Fatalf("the target block must publish the resolver agreement:\n%s", text)
	}
	if !strings.Contains(text, "base pr1 -> refs/heads/master") {
		t.Fatalf("the metadata block must show old -> new:\n%s", text)
	}
}

// TestFormatReparentPlan_BlockersAndWarningsRender proves both lists render
// with their entry scope, and that an empty list says so rather than
// disappearing.
func TestFormatReparentPlan_BlockersAndWarningsRender(t *testing.T) {
	plan := reparentSamplePlan()
	empty, err := FormatReparentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), "blockers: none") || !strings.Contains(string(empty), "warnings: none") {
		t.Fatalf("empty lists must be stated explicitly:\n%s", empty)
	}

	entry := "pr3"
	plan.Blockers = []ReparentPlanBlocker{{Kind: ReparentRefusalCutoffAbsent, Entry: &entry, Detail: "no boundary"}}
	plan.Warnings = []ReparentPlanWarning{{Kind: ReparentWarnHolderDetachRequired, Detail: "will detach"}}
	out, err := FormatReparentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "cutoff-absent [pr3]: no boundary") {
		t.Fatalf("blocker rendering:\n%s", out)
	}
	if !strings.Contains(string(out), "holder-detach-required: will detach") {
		t.Fatalf("warning rendering:\n%s", out)
	}
}

// TestReparentRenderers_HonourTheEncodeFaultSeam proves neither renderer
// writes a partial document when encoding itself fails.
func TestReparentRenderers_HonourTheEncodeFaultSeam(t *testing.T) {
	PlanEncodeFault = func() error { return io.ErrUnexpectedEOF }
	t.Cleanup(func() { PlanEncodeFault = nil })

	if _, err := MarshalReparentPlan(reparentSamplePlan()); err != io.ErrUnexpectedEOF {
		t.Fatalf("MarshalReparentPlan error = %v", err)
	}
	if _, err := FormatReparentPlan(reparentSamplePlan()); err != io.ErrUnexpectedEOF {
		t.Fatalf("FormatReparentPlan error = %v", err)
	}
}

// TestMarshalRebasePlan_StillNormalizesItsOwnArrays is the frozen-surface
// half: the sync renderer is untouched by this feature.
func TestMarshalRebasePlan_StillNormalizesItsOwnArrays(t *testing.T) {
	out, err := MarshalRebasePlan(RebasePlan{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"blockers":[]`) {
		t.Fatalf("the sync document's normalization changed:\n%s", out)
	}
}
