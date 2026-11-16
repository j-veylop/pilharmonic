package anim

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderIsDeterministicPerSettings(t *testing.T) {
	settings := Settings{ID: "tool", Size: 4, Label: "read", LabelColor: lipgloss.Color("#ffffff"), GradColorA: lipgloss.Color("#ff0000"), GradColorB: lipgloss.Color("#0000ff")}
	first, second := New(settings), New(settings)
	if ansi.Strip(first.Render()) != ansi.Strip(second.Render()) {
		t.Fatalf("same settings rendered differently: %q vs %q", first.Render(), second.Render())
	}
	if first.Width() != 4+1+len("read")+3 {
		t.Fatalf("width = %d", first.Width())
	}
}

func TestAdvanceAnimatesScrambleAndEllipsis(t *testing.T) {
	spinner := New(Settings{ID: "a", Size: 1, GradColorA: lipgloss.Color("#ff0000"), GradColorB: lipgloss.Color("#00ff00")})
	before := ansi.Strip(spinner.Render())
	for i := 0; i < maxBirthSteps+2; i++ {
		spinner.Advance()
	}
	if after := ansi.Strip(spinner.Render()); after == before {
		t.Fatal("scramble frames did not advance")
	}
	labelled := New(Settings{ID: "b", Size: 0, NoScramble: true, Label: "thinking", LabelColor: lipgloss.Color("#ffffff")})
	frames := map[string]bool{}
	for i := 0; i < ellipsisSpeed*len(ellipsisText)*2; i++ {
		labelled.Advance()
		frames[ansi.Strip(labelled.Render())] = true
	}
	if len(frames) < 2 {
		t.Fatalf("ellipsis did not animate: %v", frames)
	}
}

func TestAnimatedSpinnerKeepsCellWidth(t *testing.T) {
	spinner := New(Settings{ID: "c", Size: 3, GradColorA: lipgloss.Color("#ff0000"), GradColorB: lipgloss.Color("#0000ff")})
	for i := 0; i < 5; i++ {
		if got := len(strings.Split(spinner.Render(), "\n")); got != 1 {
			t.Fatalf("render wrapped: %d rows", got)
		}
		spinner.Advance()
	}
}

func TestSuffixShowsAndHidesEllipsis(t *testing.T) {
	spinner := New(Settings{ID: "suffix", Size: 1, Label: "run", NoScramble: true, LabelColor: lipgloss.Color("#ffffff"), SuffixColor: lipgloss.Color("#888888")})
	spinner.Advance()
	withEllipsis := ansi.Strip(spinner.Render())
	if !strings.HasSuffix(withEllipsis, ".") {
		t.Fatalf("ellipsis missing: %q", withEllipsis)
	}
	width := spinner.Width()
	spinner.SetSuffixText("12s")
	rendered := ansi.Strip(spinner.Render())
	if !strings.HasSuffix(rendered, " 12s") || strings.HasSuffix(strings.TrimSuffix(rendered, " 12s"), ".") {
		t.Fatalf("suffix did not replace the ellipsis: %q", rendered)
	}
	if spinner.Width() != width+1+len("12s") {
		t.Fatalf("width %d, want %d", spinner.Width(), width+4)
	}
}

func TestCacheAndRebuildPreserveDeterminism(t *testing.T) {
	settings := Settings{ID: "cache", Size: 4, Label: "work", LabelColor: lipgloss.Color("#ffffff"), GradColorA: lipgloss.Color("#ff0000"), GradColorB: lipgloss.Color("#0000ff")}
	first := New(settings)
	for i := 0; i < 12; i++ {
		first.Advance()
	}
	second := New(settings)
	for i := 0; i < 12; i++ {
		second.Advance()
	}
	if ansi.Strip(first.Render()) != ansi.Strip(second.Render()) {
		t.Fatalf("cache changed the frames: %q vs %q", first.Render(), second.Render())
	}
	before := ansi.Strip(first.Render())
	first.SetColors(lipgloss.Color("#ffff00"), lipgloss.Color("#00ff00"), lipgloss.Color("#0000ff"), lipgloss.Color("#aabbcc"))
	if ansi.Strip(first.Render()) != before {
		t.Fatal("theme rebuild restarted the animation")
	}
	first.SetLabel("review")
	if first.Width() == second.Width() {
		t.Fatal("label change did not update the width")
	}
}
