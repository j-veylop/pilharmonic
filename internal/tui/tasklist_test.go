package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func taskIDs(rows []taskListRow) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.task.ID
	}
	return ids
}

func TestMainRowsGroupByActionState(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "done", Status: orchestrator.Completed},
		{ID: "queued", Status: orchestrator.Queued},
		{ID: "running", Status: orchestrator.Running},
		{ID: "review", Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending},
		{ID: "done-2", Status: orchestrator.Completed},
	}
	m := model{tasks: tasks}
	got := strings.Join(taskIDs(m.mainRows()), ",")
	if want := "review,running,queued"; got != want {
		t.Fatalf("collapsed rows = %q, want %q", got, want)
	}
	m.doneExpanded = true
	got = strings.Join(taskIDs(m.mainRows()), ",")
	if want := "review,running,queued,done,done-2"; got != want {
		t.Fatalf("expanded rows = %q, want %q", got, want)
	}
}

func TestMainRowsKeepDoneVisibleWhenIdle(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "done", Status: orchestrator.Completed},
		{ID: "done-2", Status: orchestrator.Completed},
	}
	m := model{tasks: tasks}
	if got := len(m.mainRows()); got != 2 {
		t.Fatalf("idle board shows %d rows, want all 2", got)
	}
}

func TestToggleDoneMovesHiddenSelection(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "running", Status: orchestrator.Running},
		{ID: "done", Status: orchestrator.Completed},
	}
	m := model{tasks: tasks, selected: 1, doneExpanded: true}
	m.toggleDone()
	if m.doneExpanded || m.selected != 0 {
		t.Fatalf("collapse left expanded=%v selected=%d, want false/0", m.doneExpanded, m.selected)
	}
	if got := ansi.Strip(m.taskPanel(60, 20)); !strings.Contains(got, "DONE 1") || !strings.Contains(got, "E expand") {
		t.Fatalf("collapsed board lost the done summary: %q", got)
	}
}

func TestTaskPanelShowsGroupHeadings(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "done", Title: "done work", Status: orchestrator.Completed},
		{ID: "running", Title: "running work", Status: orchestrator.Running},
		{ID: "review", Title: "review work", Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending},
	}
	m := model{tasks: tasks, width: 100, height: 30}
	plain := ansi.Strip(m.taskPanel(80, 24))
	for _, want := range []string{"NEED ACTION", "RUNNING", "DONE 1 · merged 0"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("board lost %q: %q", want, plain)
		}
	}
}

func TestMainTaskMetaLineCarriesLiveDetail(t *testing.T) {
	now := time.Now()
	task := orchestrator.Task{
		ID: "task", Title: "work", Agent: "general", Status: orchestrator.Running,
		Isolated: true, ExecutionWorkspace: "/tmp/worktree",
		Attempts: []orchestrator.Attempt{{ID: "a", StartedAt: now.Add(-4 * time.Minute), RequestedModel: "provider/gpt-4.1"}},
		Activities: []orchestrator.ToolActivity{{
			ID: "call", Name: "bash", Status: orchestrator.ActivityRunning, StartedAt: now.Add(-9 * time.Second),
		}},
	}
	m := model{
		tasks: []orchestrator.Task{task},
		diffStats: map[string]diffStatEntry{
			task.ID: {key: diffStatKey(task), added: 120, removed: 18, at: now},
		},
	}
	plain := ansi.Strip(m.mainTaskMetaLine(task, 120))
	for _, want := range []string{glyphWorktree, "⋔ 0", "00:04:00", "gpt-4.1", "+120", "−18", "last 9s", "bash"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("meta line lost %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "isolated") || strings.Contains(plain, "subtask(s)") {
		t.Fatalf("meta line still spells out the workspace badge or subtask count: %q", plain)
	}
	if strings.Contains(plain, task.Agent) {
		t.Fatalf("meta line still names the executor %q: %q", task.Agent, plain)
	}
}

func TestMainTaskMetaLineShowsProviderAndModelGlyphs(t *testing.T) {
	task := orchestrator.Task{
		ID: "task", Status: orchestrator.Running,
		Attempts: []orchestrator.Attempt{{ID: "a", RequestedModel: "deepseek/deepseek-flash"}},
	}
	plain := ansi.Strip((model{tasks: []orchestrator.Task{task}}).mainTaskMetaLine(task, 120))
	for _, want := range []string{providerGlyphs["deepseek"], modelGlyph("deepseek-flash"), "⋔ 0"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("meta line lost %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "deepseek-flash") {
		t.Fatalf("meta line names a model that its glyphs already mark: %q", plain)
	}
}

func TestModelBadgeKeepsUnknownModelNames(t *testing.T) {
	if got, want := modelBadge("openai/gpt-6.1-unknown"), providerGlyphs["openai"]+" gpt-6.1-unknown"; got != want {
		t.Fatalf("unknown model badge = %q, want %q", got, want)
	}
	if got, want := modelBadge("deepseek/deepseek-flash"), providerGlyphs["deepseek"]+" "+modelGlyph("deepseek-flash"); got != want {
		t.Fatalf("known model badge = %q, want %q", got, want)
	}
}

// The fish and the GPT name glyphs use codepoints present in Nerd Fonts 3.4;
// the older Font Awesome fish codepoint renders as a blank cell.
func TestModelGlyphUsesInstalledNerdFontCodepoints(t *testing.T) {
	if got := providerGlyphs["deepseek"]; got != "\uee41" {
		t.Fatalf("deepseek glyph = %q, want nf-fa-fish", got)
	}
	for _, tc := range []struct {
		model, glyph string
	}{
		{"gpt-5.6-luna", "\uf186"},  // nf-fa-moon_o
		{"gpt-6.1-terra", "\uf0ac"}, // nf-fa-globe
		{"gpt-6.1-sol", "\uf185"},   // nf-fa-sun_o
		{"gpt-6.1-astra", "\uf005"}, // nf-fa-star
	} {
		if got := modelGlyph(tc.model); got != tc.glyph {
			t.Fatalf("%s glyph = %q, want %q", tc.model, got, tc.glyph)
		}
	}
}

func TestMainTaskMetaLineMarksStaleRun(t *testing.T) {
	now := time.Now()
	task := orchestrator.Task{
		ID: "task", Title: "work", Agent: "general", Status: orchestrator.Running,
		Attempts: []orchestrator.Attempt{{ID: "a", StartedAt: now.Add(-25 * time.Minute)}},
		Activities: []orchestrator.ToolActivity{{
			ID: "call", Name: "bash", Status: orchestrator.ActivityRunning, StartedAt: now.Add(-21 * time.Minute),
		}},
		UpdatedAt: now.Add(-21 * time.Minute),
	}
	plain := ansi.Strip((model{tasks: []orchestrator.Task{task}}).mainTaskMetaLine(task, 120))
	if !strings.Contains(plain, glyphWarning+" 00:25:00") {
		t.Fatalf("stale run not marked: %q", plain)
	}
}

func TestActionHintNamesReviewKeys(t *testing.T) {
	task := orchestrator.Task{Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending}
	if got := actionHint(task); !strings.Contains(got, "v review") || !strings.Contains(got, "y merge") {
		t.Fatalf("review hint = %q", got)
	}
	failed := orchestrator.Task{Status: orchestrator.Failed}
	if got := actionHint(failed); got != "r retry" {
		t.Fatalf("failed hint = %q", got)
	}
}
