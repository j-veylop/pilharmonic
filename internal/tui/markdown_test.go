package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownRenderingAndBounds(t *testing.T) {
	input := "# Heading\n\n**Bold text**\n\n- item\n\n```go\n" + strings.Repeat("verylongtoken", 20) + "\n```\n"
	for _, width := range []int{8, 20, 50} {
		lines := renderedOutput(input, width)
		plain := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(strings.Join(strings.Fields(plain), ""), "Heading") || !strings.Contains(plain, "item") || strings.Contains(plain, "```") || strings.Contains(plain, "**Bold") {
			t.Fatalf("Markdown not rendered: %q", plain)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d: %q", width, line)
			}
		}
	}
}
func TestToolActivityIsDistinctFromAssistantText(t *testing.T) {
	input := "# Plan\n\nChecks next.\n\n[running] bash: go test ./...\n[finished] bash: go test ./...\n[failed] bash: go vet ./...\n\nAll good.\n"
	lines := renderedOutput(input, 80)
	all := strings.Join(lines, "\n")
	plain := ansi.Strip(all)
	for _, want := range []string{"Plan", "Checks next", "All good", "▎", "✓ finished", "bash", "go test ./...", "✗ failed", "go vet ./..."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	for _, unwanted := range []string{"[running]", "[finished]", "[failed]", "⠿"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("raw or stale marker %q in %q", unwanted, plain)
		}
	}
	if got := strings.Count(plain, "go test ./..."); got != 1 {
		t.Fatalf("paired tool call rendered %d times: %q", got, plain)
	}
	if !strings.Contains(all, "\x1b[") {
		t.Fatal("tool activity was not styled")
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("line exceeds width: %d %q", width, line)
		}
	}
}

func TestToolActivityRunningRowStaysVisible(t *testing.T) {
	plain := ansi.Strip(strings.Join(renderedOutput("work\n[running] read: main.go\n", 60), "\n"))
	for _, want := range []string{"⠋ running", "read", "main.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
}

func TestToolActivityCompletionReplacesRowAcrossBlankLines(t *testing.T) {
	plain := ansi.Strip(strings.Join(renderedOutput("work\n\n[running] read: main.go\n\n[finished] read: main.go\n", 60), "\n"))
	if got := strings.Count(plain, "read"); got != 1 {
		t.Fatalf("completion created another row (%d occurrences): %q", got, plain)
	}
	for _, want := range []string{"✓ finished", "main.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "⠋ running") || strings.Contains(plain, runningGlyphPlaceholder) {
		t.Fatalf("running row was not completed: %q", plain)
	}
}

func TestToolActivityCompletionWithoutArgumentsReplacesRunningRow(t *testing.T) {
	// The Fantasy adapter writes the arguments only on the start marker and
	// repeats just the tool name when a call succeeds.
	plain := ansi.Strip(strings.Join(renderedOutput("work\n[running] read: {\"path\":\"note.txt\"}\n\n[finished] read\n", 80), "\n"))
	if got := strings.Count(plain, "read"); got != 1 {
		t.Fatalf("completion without arguments created another row (%d occurrences): %q", got, plain)
	}
	for _, want := range []string{"✓ finished", "note.txt"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "⠋ running") || strings.Contains(plain, runningGlyphPlaceholder) {
		t.Fatalf("running row was not completed: %q", plain)
	}
}

func TestToolActivityFailureExplanationReplacesRunningRow(t *testing.T) {
	plain := ansi.Strip(strings.Join(renderedOutput("[running] bash: go test ./...\n\n[failed] bash: exit status 1\n", 80), "\n"))
	if got := strings.Count(plain, "bash"); got != 1 {
		t.Fatalf("failure created another row (%d occurrences): %q", got, plain)
	}
	for _, want := range []string{"✗ failed", "exit status 1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "⠋ running") {
		t.Fatalf("running row was not completed: %q", plain)
	}
}

func TestToolActivitySequentialCallsOfSameToolKeepOneRowEach(t *testing.T) {
	input := "[running] read: a.go\n[finished] read\n\n[running] read: b.go\n[finished] read\n"
	plain := ansi.Strip(strings.Join(renderedOutput(input, 80), "\n"))
	if got := strings.Count(plain, "read"); got != 2 {
		t.Fatalf("sequential calls collapsed together (%d occurrences): %q", got, plain)
	}
	for _, want := range []string{"a.go", "b.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "⠋ running") {
		t.Fatalf("sequential calls left running rows: %q", plain)
	}
}

func TestRunningToolGlyphAdvancesWithSpinner(t *testing.T) {
	m := model{spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot))}
	m.spinner, _ = m.spinner.Update(m.spinner.Tick())
	glyph := m.runningGlyph()
	if glyph != spinner.MiniDot.Frames[1] {
		t.Fatalf("spinner did not advance: %q", glyph)
	}
	plain := ansi.Strip(strings.Join(renderedOutputWithGlyph("[running] read: animated.go\n", 60, glyph), "\n"))
	if !strings.Contains(plain, glyph+" running") {
		t.Fatalf("transcript did not use current frame %q: %q", glyph, plain)
	}
}

func TestToolActivityInterleavedCallsKeepOneRowPerCall(t *testing.T) {
	input := "[running] read: a.go\n[running] bash: go test ./...\n[finished] read: a.go\n[failed] bash: go test ./...\n"
	plain := ansi.Strip(strings.Join(renderedOutput(input, 80), "\n"))
	for _, want := range []string{"✓ finished", "✗ failed", "read", "bash"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if strings.Count(plain, "read") != 1 || strings.Count(plain, "bash") != 1 {
		t.Fatalf("interleaved calls rendered more than once: %q", plain)
	}
	if strings.Contains(plain, "⠋ running") {
		t.Fatalf("interleaved calls left running rows: %q", plain)
	}
}

func TestThinkingBlocksAreHiddenByDefaultAndToggleable(t *testing.T) {
	input := "Before\n[thinking]\nprivate reasoning\nsecond line\n[/thinking]\nAfter\n"
	hidden := ansi.Strip(strings.Join(renderedOutput(input, 80), "\n"))
	if strings.Contains(hidden, "private reasoning") || strings.Contains(hidden, "second line") {
		t.Fatalf("hidden thinking leaked text: %q", hidden)
	}
	for _, want := range []string{"Before", "After", "thinking hidden"} {
		if !strings.Contains(hidden, want) {
			t.Fatalf("missing %q in %q", want, hidden)
		}
	}
	shown := ansi.Strip(strings.Join(renderedOutputWithThinking(input, 80, defaultRunningGlyph, true), "\n"))
	for _, want := range []string{"THINKING", "private reasoning", "second line", "Before", "After"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("missing %q in %q", want, shown)
		}
	}
	if strings.Contains(shown, "[thinking]") || strings.Contains(shown, "[/thinking]") {
		t.Fatalf("raw marker leaked: %q", shown)
	}
}

func TestThinkingBlocksAcceptClosingMarkerVariantsAndTruncatedTail(t *testing.T) {
	for _, input := range []string{
		"start\n[thinking]\nsecret\n[thinking end]\nend\n",
		"start\n[thinking_start]\nsecret\n[thinking_end]\nend\n",
		"start\n[thinking] secret\n[thinking] second secret\nend\n",
	} {
		hidden := ansi.Strip(strings.Join(renderedOutput(input, 60), "\n"))
		if strings.Contains(hidden, "secret") || !strings.Contains(hidden, "start") || !strings.Contains(hidden, "end") {
			t.Fatalf("marker variant failed: %q", hidden)
		}
		shown := ansi.Strip(strings.Join(renderedOutputWithThinking(input, 60, defaultRunningGlyph, true), "\n"))
		if !strings.Contains(shown, "secret") || !strings.Contains(shown, "THINKING") {
			t.Fatalf("shown marker variant failed: %q", shown)
		}
	}
	truncated := ansi.Strip(strings.Join(renderedOutput("old tail\n[/thinking]\nvisible\n", 60), "\n"))
	if strings.Contains(truncated, "[/thinking]") || !strings.Contains(truncated, "visible") {
		t.Fatalf("truncated tail failed: %q", truncated)
	}
}

func TestMarkdownSanitizesControlsAndPreservesLogs(t *testing.T) {
	lines := renderedOutput("line-01\nline-02\n\x1b[999Cunsafe\ttext", 40)
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "\x1b[999C") || strings.Contains(all, "\t") {
		t.Fatalf("unsafe control: %q", all)
	}
	first, second := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "line-01") {
			first = i
		}
		if strings.Contains(line, "line-02") {
			second = i
		}
	}
	if first < 0 || second <= first {
		t.Fatalf("log lines collapsed: %q", all)
	}
	lines[0] = "mutated"
	if renderedOutput("line-01\nline-02\n\x1b[999Cunsafe\ttext", 40)[0] == "mutated" {
		t.Fatal("caller mutated cache")
	}
}
