package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pilharmonic/internal/orchestrator"
)

func TestShippedProfilesUseBuiltInCoordination(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	s.LoadBuiltinSpecialists()
	if err := loadStartupProfiles(s, "profiles.json", false); err != nil {
		t.Fatal(err)
	}
	profile, ok := s.GetProfile(orchestrator.CoordinatorProfileName)
	if !ok || profile.Model == "" {
		t.Fatalf("shipped coordinator profile not loaded: %+v", profile)
	}
	if profile.CoordinatorInstructions != "" {
		t.Fatalf("shipped profile duplicates compiled coordination guidance: %q", profile.CoordinatorInstructions)
	}
}

func TestStartupProfiles(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	// An empty path is a no-op.
	if err := loadStartupProfiles(s, "", true); err != nil {
		t.Fatal(err)
	}
	// A missing default is ignored when not required.
	if err := loadStartupProfiles(s, "profiles.json", false); err != nil {
		t.Fatalf("missing non-required path: %v", err)
	}
	if err := os.WriteFile("profiles.json", []byte(`{"profiles":[{"name":"local"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, "profiles.json", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetProfile("local"); !ok {
		t.Fatal("profile from profiles.json missing")
	}
	if err := os.WriteFile("profiles.json", []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, "profiles.json", false); err == nil {
		t.Fatal("invalid profiles file ignored")
	}
	if err := os.WriteFile("override.json", []byte(`{"profiles":[{"name":"override"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, "override.json", true); err != nil {
		t.Fatal(err)
	}
	// An explicitly required path that is missing must report an error.
	if err := loadStartupProfiles(s, "missing.json", true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit path: %v", err)
	}
}

func TestVersionStringIncludesCommit(t *testing.T) {
	oldVersion, oldCommit := version, commit
	defer func() { version, commit = oldVersion, oldCommit }()
	version, commit = "v1.2.3", "abc123"
	if got := versionString(); got != "v1.2.3 (abc123)" {
		t.Fatalf("version string %q", got)
	}
	commit = ""
	if got := versionString(); got != "v1.2.3" {
		t.Fatalf("version string %q", got)
	}
}
