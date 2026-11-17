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
