package orchestrator

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReportsPersistAndDoNotChangeMandate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("task", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordReport(task.ID, "blocked", "Need credentials"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Stage() != "blocked" || got.Prompt != "original mandate" {
		t.Fatalf("bad report: %+v", got)
	}
	got.Reports[0].Message = "changed"
	fresh, _ := s.GetTask(task.ID)
	if fresh.Reports[0].Message != "Need credentials" {
		t.Fatal("mutable snapshot")
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := again.GetTask(task.ID)
	if restored.Stage() != "blocked" {
		t.Fatal("report lost")
	}
	if err = s.RecordReport(task.ID, "unknown", "x"); err == nil {
		t.Fatal("invalid kind accepted")
	}
}

func TestStagesRespectAttemptsAndContext(t *testing.T) {
	now := time.Now().UTC()
	task := Task{Status: Running, Reports: []Report{{Kind: "blocked", CreatedAt: now}}}
	if task.Stage() != "blocked" {
		t.Fatal(task.Stage())
	}
	task.Context = []Trigger{{CreatedAt: now.Add(time.Second), Delivery: "accepted"}}
	if task.Stage() != "working" {
		t.Fatal(task.Stage())
	}
	task.Reports = append(task.Reports, Report{Kind: "ready", CreatedAt: now.Add(2 * time.Second)})
	if task.Stage() != "awaiting review" {
		t.Fatal(task.Stage())
	}
	task.Attempts = []Attempt{{StartedAt: now.Add(3 * time.Second)}}
	if task.Stage() != "working" {
		t.Fatal("old report dominates new attempt")
	}
	task.Context = append(task.Context, Trigger{CreatedAt: now.Add(4 * time.Second), Delivery: "unknown"})
	if task.Stage() != "input undelivered" {
		t.Fatal(task.Stage())
	}
}
