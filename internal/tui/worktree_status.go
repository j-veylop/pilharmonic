package tui

import (
	"github.com/charmbracelet/lipgloss"
	"orchestrator/internal/orchestrator"
)

// Nerd Font glyphs keep workspace and integration state compact in task rows.
// Every glyph is paired with a text label so the meaning survives a terminal
// without Nerd Fonts installed.
const (
	glyphWorktree   = "\ue725" // nf-oct-git_branch
	glyphMerged     = "\ue727" // nf-oct-git_merge
	glyphCheck      = "\uf00c" // nf-fa-check
	glyphConflict   = "\uf071" // nf-fa-exclamation_triangle
	glyphMergeError = "\uf00d" // nf-fa-times
	glyphShared     = "\uf07b" // nf-fa-folder
	glyphInherited  = "\uf0c1" // nf-fa-link
)

type workspaceBadge struct {
	glyph string
	label string
	color lipgloss.Color
}

// workspaceBadgeFor summarizes where a task runs and what happened to its
// isolated work. Merge results take precedence over the worktree branch because
// they describe the final integration state; a conflict remains distinct from
// other merge failures so it can be recognized at a glance.
func workspaceBadgeFor(task orchestrator.Task) workspaceBadge {
	switch {
	case task.MergeStatus == orchestrator.MergeFailed && task.MergeConflict():
		return workspaceBadge{glyphConflict, "conflict", lipgloss.Color("#FF8F9B")}
	case task.MergeStatus == orchestrator.MergeFailed:
		return workspaceBadge{glyphMergeError, "merge error", lipgloss.Color("#FF8F9B")}
	case task.MergeStatus == orchestrator.MergeMerged:
		return workspaceBadge{glyphMerged, "merged", lipgloss.Color("#98DAA6")}
	case task.MergeStatus == orchestrator.MergeUpToDate:
		return workspaceBadge{glyphCheck, "up to date", lipgloss.Color("#98DAA6")}
	case task.Isolated:
		return workspaceBadge{glyphWorktree, "isolated", lipgloss.Color("#79D8C3")}
	case task.ParentID != "":
		return workspaceBadge{glyphInherited, "inherited", muted}
	default:
		return workspaceBadge{glyphShared, "shared", muted}
	}
}

func (b workspaceBadge) render() string {
	return lipgloss.NewStyle().Foreground(b.color).Render(b.glyph) + mutedStyle.Render(" "+b.label)
}

func (b workspaceBadge) glyphOnly() string {
	return lipgloss.NewStyle().Foreground(b.color).Render(b.glyph)
}
