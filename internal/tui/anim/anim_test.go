package anim

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestCyclingGradientWrapsWithoutAColorJump(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	start, end := lipgloss.Color("#d94f9c"), lipgloss.Color("#ff9dd8")
	frames := renderFrames(Settings{CycleColors: true, GradColorA: start, GradColorB: end}, 15).initial
	paint := func(c lipgloss.Color) string { return lipgloss.NewStyle().Foreground(c).Render(".") }
	if frames[0][0] != paint(start) || frames[len(frames)/2][0] != paint(end) {
		t.Fatal("cycle did not visit both gradient endpoints")
	}
	last, first := frames[len(frames)-1], frames[0]
	if last[0] != first[1] || last[0] == first[0] {
		t.Fatal("cycle did not return to the start color one step at a time")
	}
	for i := 1; i < len(last); i++ {
		if last[i] != first[i-1] {
			t.Fatalf("color at cell %d broke the one-cell shift across the loop", i)
		}
	}
}

func TestMultiStopGradientLoopsThroughEveryHue(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	blue, pink, violet := lipgloss.Color("#00a4ff"), lipgloss.Color("#ff60ff"), lipgloss.Color("#8b75ff")
	frames := renderFrames(Settings{CycleColors: true, GradColors: []lipgloss.TerminalColor{blue, pink, violet}}, 15).initial
	first := frames[0]
	// Stops are sampled with float math, so allow a step of rounding per channel.
	if !nearColor(first[0], "#00a4ff") || !nearColor(first[10], "#ff60ff") || !nearColor(frames[20][0], "#8b75ff") {
		t.Fatal("loop did not visit blue, pink and violet in order")
	}
	last := frames[len(frames)-1]
	for i := 1; i < len(last); i++ {
		if last[i] != first[i-1] {
			t.Fatalf("color at cell %d broke the one-cell shift across the loop", i)
		}
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

// nearColor reports whether a rendered cell's truecolor foreground is within two
// 8-bit steps of hex on every channel.
func nearColor(cell, hex string) bool {
	m := sgrRGB.FindStringSubmatch(cell)
	if m == nil {
		return false
	}
	var want, got [3]int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &want[0], &want[1], &want[2]); err != nil {
		return false
	}
	for i := range got {
		if _, err := fmt.Sscan(m[i+1], &got[i]); err != nil {
			return false
		}
		if d := got[i] - want[i]; d > 2 || d < -2 {
			return false
		}
	}
	return true
}

var sgrRGB = regexp.MustCompile(`38;2;(\d+);(\d+);(\d+)m`)

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
	if first.Width() != 4+1+len("work")+3 {
		t.Fatalf("width = %d", first.Width())
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
