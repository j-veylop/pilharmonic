package orchestrator

import (
	"context"
	"io"
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

func TestAdapterReportIsAttemptScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	release := make(chan struct{})
	writers := make(chan outputWriter, 1)
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, out io.Writer) error {
		writers <- out.(outputWriter)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	writer := <-writers
	if err = writer.RecordReport("progress", "halfway"); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetTask(task.ID)
	if len(current.Reports) != 1 {
		t.Fatalf("adapter report missing: %+v", current.Reports)
	}
	report := current.Reports[0]
	if report.Source != "adapter" || report.Kind != "progress" || report.AttemptID == "" || report.AttemptID != current.currentAttemptID() {
		t.Fatalf("bad adapter report: %+v", report)
	}
	if current.Stage() != "working" {
		t.Fatalf("progress report changed attention stage: %s", current.Stage())
	}
	close(release)
	await(t, s, task.ID, Completed)
	if err = writer.RecordReport("progress", "too late"); err == nil {
		t.Fatal("adapter reported after the attempt ended")
	}
	if _, err = s.FollowUp(task.ID, "follow-up"); err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, out io.Writer) error {
		writers <- out.(outputWriter)
		<-ctx.Done()
		return ctx.Err()
	}))
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	currentWriter := <-writers
	if err = writer.RecordReport("progress", "stale"); err == nil {
		t.Fatal("an old adapter reported for the new attempt")
	}
	if _, err = writer.Write([]byte("stale")); err == nil {
		t.Fatal("an old adapter wrote output for the new attempt")
	}
	if err = writer.RecordToolActivity(ToolActivity{ID: "stale"}); err == nil {
		t.Fatal("an old adapter recorded a tool call for the new attempt")
	}
	if err = currentWriter.RecordReport("progress", "second attempt"); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentReportIgnoresAnotherAttempt(t *testing.T) {
	now := time.Now().UTC()
	task := Task{
		Status:   Running,
		Attempts: []Attempt{{ID: "old", StartedAt: now}, {ID: "new", StartedAt: now.Add(time.Minute)}},
		Reports:  []Report{{Kind: "ready", Source: "adapter", AttemptID: "old", CreatedAt: now.Add(2 * time.Minute)}},
	}
	if _, ok := task.CurrentReport(); ok {
		t.Fatal("report from an earlier attempt governed the current attempt")
	}
	task.Reports = append(task.Reports, Report{Kind: "blocked", Source: "adapter", AttemptID: "new", CreatedAt: now.Add(3 * time.Minute)})
	if report, ok := task.CurrentReport(); !ok || report.Kind != "blocked" {
		t.Fatalf("current adapter report not selected: %+v", report)
	}
}
