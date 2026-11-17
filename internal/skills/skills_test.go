package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsSkillMetadataAndInstructions(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", `---
name: release
description: >
  Build and publish a tagged release
  without skipping verification.
allowed-tools: bash, read
---
# Release

Run the release script.
`)
	writeSkill(t, dir, "audit", `---
name: audit
description: 'Review changes for bugs.'
---
Review the diff.
`)
	catalog, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 2 {
		t.Fatalf("loaded %d skills, want 2", catalog.Len())
	}
	list := catalog.List()
	if list[0].Name != "audit" || list[1].Name != "release" {
		t.Fatalf("skills not sorted: %v", list)
	}
	release, ok := catalog.Lookup("release")
	if !ok {
		t.Fatal("release skill missing")
	}
	if release.Description != "Build and publish a tagged release without skipping verification." {
		t.Fatalf("folded description = %q", release.Description)
	}
	if !strings.Contains(release.Body, "Run the release script.") {
		t.Fatalf("body = %q", release.Body)
	}
	if release.Path == "" {
		t.Fatal("skill path was not recorded")
	}
	prompt := catalog.Prompt()
	for _, want := range []string{"skill tool", "release: Build and publish", "audit: Review changes"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q missing %q", prompt, want)
		}
	}
}

func TestLoadMissingDirectoryReportsNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}

func TestLoadSkipsDirectoriesWithoutSkillFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(dir)
	if err != nil || catalog.Len() != 0 {
		t.Fatalf("catalog = %v, error = %v", catalog, err)
	}
}

func TestLoadRejectsMalformedOrDuplicateSkills(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
	}{
		{"missing frontmatter", map[string]string{"broken": "# no metadata\n"}},
		{"missing description", map[string]string{"broken": "---\nname: broken\n---\nbody\n"}},
		{"missing body", map[string]string{"broken": "---\nname: broken\ndescription: x\n---\n"}},
		{"duplicate name", map[string]string{
			"one": "---\nname: same\ndescription: one\n---\nbody\n",
			"two": "---\nname: same\ndescription: two\n---\nbody\n",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range test.files {
				writeSkill(t, dir, name, text)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEmptyCatalogIsUsable(t *testing.T) {
	var catalog Catalog
	if catalog.Len() != 0 || catalog.Prompt() != "" {
		t.Fatal("zero catalog is not empty")
	}
	if _, ok := catalog.Lookup("missing"); ok {
		t.Fatal("empty catalog returned a skill")
	}
}
