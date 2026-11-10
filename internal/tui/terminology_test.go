package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestMainViewUsesPlainRequestTaskAndActionTerms(t *testing.T) {
	blocked := orchestrator.Task{
		ID: "blocked", Title: "blocked work", Prompt: "work", Status: orchestrator.Running,
		Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: time.Now()}},
	}
	board := ansi.Strip((model{width: 120, height: 35, tasks: []orchestrator.Task{blocked}}).View())
	for _, want := range []string{"TASKS", "◆ 1 needs action", "/ request", "! action"} {
		if !strings.Contains(board, want) {
			t.Fatalf("task board missing %q: %s", want, board)
		}
	}
	for _, unwanted := range []string{"/ route", "! attention"} {
		if strings.Contains(board, unwanted) {
			t.Fatalf("task board kept jargon %q: %s", unwanted, board)
		}
	}

	router := ansi.Strip((model{router: true, width: 120, height: 35, tasks: []orchestrator.Task{blocked}}).View())
	for _, want := range []string{"NEW REQUEST", "NEEDS ACTION", "RECENT TASKS", "enter submit"} {
		if !strings.Contains(router, want) {
			t.Fatalf("request form missing %q: %s", want, router)
		}
	}
	for _, unwanted := range []string{"ROUTE WORK", "NEEDS ATTENTION", "RECENT ROUTES", "enter route"} {
		if strings.Contains(router, unwanted) {
			t.Fatalf("request form kept jargon %q: %s", unwanted, router)
		}
	}

	notice := routeNotice(blocked)
	if !strings.Contains(notice, "Request accepted") || strings.Contains(notice, "Routed to") {
		t.Fatalf("acceptance notice still describes routing: %q", notice)
	}
}

func TestHelpStartsWithMainViewConcepts(t *testing.T) {
	if len(helpTopics) == 0 || helpTopics[0].title != "Concepts" {
		t.Fatalf("help does not lead with concepts: %+v", helpTopics)
	}
	m := model{width: 120, height: 40, help: true}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"Request", "Task", "Route", "Action", "Accepted work", "need you"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("concepts help missing %q: %s", want, plain)
		}
	}
}
