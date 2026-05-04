package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersionPrefersInjectedVersion(t *testing.T) {
	got := resolveVersion("v1.2.3", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true
	})
	if got != "v1.2.3" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestResolveVersionUsesModuleVersion(t *testing.T) {
	got := resolveVersion("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}, true
	})
	if got != "v0.3.0" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "v0.3.0")
	}
}

func TestResolveVersionKeepsDevForDevelBuild(t *testing.T) {
	got := resolveVersion("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	})
	if got != "dev" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "dev")
	}
}

func TestResolveVersionKeepsDevWithoutBuildInfo(t *testing.T) {
	got := resolveVersion("dev", func() (*debug.BuildInfo, bool) {
		return nil, false
	})
	if got != "dev" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "dev")
	}
}
