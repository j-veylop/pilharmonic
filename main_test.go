package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"orchestrator/internal/orchestrator"
)

func TestStartupProfiles(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if err := loadStartupProfiles(s, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("profiles.json", []byte(`{"profiles":[{"name":"local"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetProfile("local"); !ok {
		t.Fatal("profile from current directory missing")
	}
	if err := os.WriteFile("profiles.json", []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, ""); err == nil {
		t.Fatal("invalid local file ignored")
	}
	if err := os.WriteFile("override.json", []byte(`{"profiles":[{"name":"override"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, "override.json"); err != nil {
		t.Fatal(err)
	}
	if err := loadStartupProfiles(s, "missing.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit path: %v", err)
	}
}
