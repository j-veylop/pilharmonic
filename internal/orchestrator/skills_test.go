package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestSkill(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureLoadsOptionalSkillsDirectory(t *testing.T) {
	dir := t.TempDir()
	s := openTestService(t, filepath.Join(dir, "state.json"))
	options := Options{MaxConcurrent: 1}
	if _, err := s.Configure(options); err != nil {
		t.Fatalf("missing default skills: %v", err)
	}
	if path, names := s.SkillInfo(); len(names) != 0 || path == "" {
		t.Fatalf("empty skills: %q %v", path, names)
	}
	skillsDir := filepath.Join(dir, "skills")
	writeTestSkill(t, skillsDir, "verify", "---\nname: verify\ndescription: Verify a change.\n---\nRun the checks.\n")
	if _, err := s.Configure(options); err != nil {
		t.Fatal(err)
	}
	path, names := s.SkillInfo()
	if path != skillsDir || len(names) != 1 || names[0] != "verify" {
		t.Fatalf("skills = %q %v", path, names)
	}
	runner, ok := s.runners[DefaultRouteAgent].(FantasyRunner)
	if !ok || runner.skills.Len() != 1 {
		t.Fatalf("general runner did not receive the skill catalog: %+v", runner)
	}
	prompt := runner.systemPrompt(nil)
	for _, want := range []string{"skill tool", "verify: Verify a change."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt %q missing %q", prompt, want)
		}
	}
	options.SkillsPath = filepath.Join(dir, "missing")
	if _, err := s.Configure(options); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit skills path: %v", err)
	}
}

func TestConfigureRejectsMalformedSkillsConfig(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(filepath.Join(dir, "skills.json"), []byte(`{"disabled":`), 0600); err != nil {
		t.Fatal(err)
	}
	s := openTestService(t, statePath)
	if _, err := s.Configure(Options{MaxConcurrent: 1}); err == nil {
		t.Fatal("malformed skills config was accepted")
	}
}

func TestAgentSkillSelectionPersistsIndependently(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	s := openTestService(t, statePath)
	skillsDir := filepath.Join(dir, "skills")
	writeTestSkill(t, skillsDir, "verify", "---\nname: verify\ndescription: Verify.\n---\nRun checks.\n")
	if _, err := s.Configure(Options{MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentSkillEnabled("general", "verify", false); err != nil {
		t.Fatal(err)
	}
	if s.ListAgentSkills("general")[0].Enabled || !s.ListAgentSkills("tester")[0].Enabled {
		t.Fatal("agent selection leaked")
	}
	if s.runners["general"].(FantasyRunner).skills.Len() != 0 || s.runners["tester"].(FantasyRunner).skills.Len() != 1 {
		t.Fatal("runner catalogs do not reflect agent selection")
	}
	if err := s.SetSkillEnabled("verify", false); err != nil {
		t.Fatal(err)
	}
	if !s.ListAgentSkills("tester")[0].GlobalDisabled {
		t.Fatal("global disable did not apply")
	}
	must(t, s.Shutdown())
	reopened := openTestService(t, statePath)
	defer func() { _ = reopened.Shutdown() }()
	if _, err := reopened.Configure(Options{MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	if reopened.ListAgentSkills("general")[0].Enabled {
		t.Fatal("agent disable not durable")
	}
}

func TestSetSkillEnabledPersistsAndFiltersCatalog(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	s := openTestService(t, statePath)
	skillsDir := filepath.Join(dir, "skills")
	writeTestSkill(t, skillsDir, "verify", "---\nname: verify\ndescription: Verify a change.\n---\nRun the checks.\n")
	writeTestSkill(t, skillsDir, "audit", "---\nname: audit\ndescription: Audit a change.\n---\nInspect the diff.\n")
	if _, err := s.Configure(Options{MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	if infos := s.ListSkills(); len(infos) != 2 || !infos[0].Enabled || !infos[1].Enabled {
		t.Fatalf("initial skills = %+v", infos)
	}
	if err := s.SetSkillEnabled("verify", false); err != nil {
		t.Fatal(err)
	}
	infos := s.ListSkills()
	if infos[0].Name != "audit" || !infos[0].Enabled || infos[1].Name != "verify" || infos[1].Enabled {
		t.Fatalf("skills after disabling verify = %+v", infos)
	}
	runner := s.runners[DefaultRouteAgent].(FantasyRunner)
	if _, ok := runner.skills.Lookup("verify"); ok {
		t.Fatal("disabled skill stayed in the runner catalog")
	}
	if _, ok := runner.skills.Lookup("audit"); !ok {
		t.Fatal("enabled skill disappeared from the runner catalog")
	}
	prompt := runner.systemPrompt(nil)
	if strings.Contains(prompt, "verify:") || !strings.Contains(prompt, "audit:") {
		t.Fatalf("system prompt did not follow activation: %q", prompt)
	}
	if err := s.SetSkillEnabled("missing", false); err == nil {
		t.Fatal("unknown skill was accepted")
	}
	must(t, s.Shutdown())

	reopened := openTestService(t, statePath)
	defer func() { _ = reopened.Shutdown() }()
	if _, err := reopened.Configure(Options{MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	if infos := reopened.ListSkills(); len(infos) != 2 || infos[1].Name != "verify" || infos[1].Enabled {
		t.Fatalf("persisted skills = %+v", infos)
	}
	if err := reopened.SetSkillEnabled("verify", true); err != nil {
		t.Fatal(err)
	}
	if infos := reopened.ListSkills(); !infos[1].Enabled {
		t.Fatalf("re-enabled skill = %+v", infos)
	}
}
