package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
	"github.com/j-veylop/pilharmonic/internal/tui/editor"
)

func TestAttachmentsComposeIntoTheRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("attached body"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := boardWithTask()
	m.service.(*fakeLoginService).anyConfigured = true
	m.router = routerState{open: true, Editor: editor.Editor{Text: "do it", Cursor: 5}}
	if err := m.attach(path); err != nil {
		t.Fatal(err)
	}
	if plain := ansi.Strip(strings.Join(m.composerBlock(m.composerHeight()), "\n")); !strings.Contains(plain, "note.txt") {
		t.Fatalf("attachment chip missing: %q", plain)
	}
	_, cmd := m.submitRequest(false)
	msg := cmd()
	result, ok := msg.(routerResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("submit result: %#v", msg)
	}
	service := m.service.(*fakeLoginService)
	if len(service.submitCalls) != 1 || !strings.Contains(service.submitCalls[0], "attached body") {
		t.Fatalf("attachment not in mandate: %#v", service.submitCalls)
	}
}

func TestAttachmentValidation(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, make([]byte, maxAttachmentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	var m model
	if err := m.attach(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file attached")
	}
	if err := m.attach(big); err == nil {
		t.Fatal("oversized file attached")
	}
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.attach(small); err != nil {
		t.Fatal(err)
	}
	if err := m.attach(small); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	m.detach(small)
	if len(m.attachments) != 0 {
		t.Fatalf("detach kept %v", m.attachments)
	}
}

func TestThemeCommandCycles(t *testing.T) {
	t.Cleanup(func() { ApplyTheme("auto") })
	m := model{theme: "auto"}
	m = m.slashCommand("/theme dark")
	if m.theme != "dark" {
		t.Fatalf("theme = %q", m.theme)
	}
	m = m.slashCommand("/theme")
	if m.theme != "light" {
		t.Fatalf("theme cycle = %q", m.theme)
	}
	m = m.slashCommand("/theme sparkle")
	if !strings.Contains(m.notice.text, "Usage: /theme") {
		t.Fatalf("invalid theme accepted: %q", m.notice.text)
	}
}

func TestIconModeSwapsStatusGlyphs(t *testing.T) {
	task := orchestrator.Task{ID: "task", Status: orchestrator.Completed}
	if got := ansi.Strip((model{}).taskStatusIcon(task)); got != "✓" {
		t.Fatalf("default glyph = %q", got)
	}
	if got := ansi.Strip((model{icons: true}).taskStatusIcon(task)); got != "\uf00c" {
		t.Fatalf("icon glyph = %q", got)
	}
}

func TestGradientAnimationKeepsTextAndChangesColor(t *testing.T) {
	if a, b := gradientColor(0, 12, 0), gradientColor(0, 12, 3); a == b {
		t.Fatal("animation did not shift the gradient")
	}
	if a, b := gradientColor(0, 12, 0), gradientColor(1, 12, 0); a == b {
		t.Fatal("gradient is flat")
	}
}

func TestDiffCardRendersOnlyDiffs(t *testing.T) {
	if card := (model{}).diffCard("just prose\nwith - a minus", 40); card != nil {
		t.Fatalf("prose rendered as a diff: %q", card)
	}
	diff := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n"
	card := ansi.Strip(strings.Join((model{}).diffCard(diff, 40), "\n"))
	for _, want := range []string{"diff", "+new", "-old", "╭", "╰"} {
		if !strings.Contains(card, want) {
			t.Fatalf("diff card missing %q: %q", want, card)
		}
	}
}
