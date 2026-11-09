package orchestrator

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestQueuedContextGetsDeliveryRecordAtStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(task.ID, "context-1", "existing style"); err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if len(done.Deliveries) != 1 {
		t.Fatalf("expected one delivery record: %+v", done.Deliveries)
	}
	delivery := done.Deliveries[0]
	if delivery.TriggerID != "context-1" || delivery.State != DeliveryAtStart || delivery.AttemptID == "" || delivery.AttemptID != done.Attempts[0].ID {
		t.Fatalf("unexpected delivery record: %+v", delivery)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := reloaded.GetTask(task.ID)
	if len(restored.Deliveries) != 1 || restored.Deliveries[0].State != DeliveryAtStart {
		t.Fatalf("delivery record not persisted: %+v", restored.Deliveries)
	}
}

func TestLiveDeliveryRecordTracksAcknowledgement(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	runner := liveTestRunner{make(chan struct{}), make(chan Trigger, 1), make(chan struct{})}
	s.RegisterRunner("pi", runner)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := s.AddContext(task.ID, "live-1", "context"); err != nil {
		t.Fatal(err)
	}
	<-runner.received
	close(runner.release)
	done := await(t, s, task.ID, Completed)
	if len(done.Deliveries) != 1 {
		t.Fatalf("expected one delivery record: %+v", done.Deliveries)
	}
	delivery := done.Deliveries[0]
	if delivery.TriggerID != "live-1" || delivery.State != DeliveryAccepted || delivery.AttemptID != done.Attempts[0].ID {
		t.Fatalf("unexpected delivery record: %+v", delivery)
	}
	if done.Context[0].Delivery != DeliveryAccepted {
		t.Fatalf("context summary not updated: %+v", done.Context)
	}
}

func TestUnacknowledgedDeliveryRecordBecomesUnknown(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := unackedRunner{make(chan struct{}), make(chan struct{})}
	s.RegisterRunner("pi", r)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-r.started
	if err := s.AddContext(task.ID, "unacked", "context"); err != nil {
		t.Fatal(err)
	}
	<-r.received
	done := await(t, s, task.ID, Completed)
	if len(done.Deliveries) != 1 || done.Deliveries[0].State != DeliveryUnknown {
		t.Fatalf("pending delivery was not settled honestly: %+v", done.Deliveries)
	}
	if done.Deliveries[0].Error == "" {
		t.Fatal("unknown delivery has no reason")
	}
}

func TestRetryCreatesNewDeliveryRecordPerAttempt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error {
		runs++
		if runs == 1 {
			return errors.New("first attempt failed")
		}
		return nil
	}))
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(task.ID, "context-1", "context"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	failed := await(t, s, task.ID, Failed)
	if err := s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 2 || len(done.Deliveries) != 2 {
		t.Fatalf("expected one delivery per attempt: attempts=%d deliveries=%+v", len(done.Attempts), done.Deliveries)
	}
	if done.Deliveries[0].State != DeliveryAtStart || done.Deliveries[1].State != DeliveryAtStart {
		t.Fatalf("unexpected delivery states: %+v", done.Deliveries)
	}
	if done.Deliveries[0].AttemptID != failed.Attempts[0].ID || done.Deliveries[1].AttemptID != done.Attempts[1].ID || done.Deliveries[0].AttemptID == done.Deliveries[1].AttemptID {
		t.Fatalf("delivery records not tied to their attempts: %+v", done.Deliveries)
	}
	if done.Stage() == "input undelivered" {
		t.Fatal("a start delivery from the new attempt was treated as undelivered")
	}
}

func TestStageUsesCurrentAttemptDeliveryRecords(t *testing.T) {
	now := time.Now().UTC()
	task := Task{
		Status:   Running,
		Attempts: []Attempt{{ID: "old", StartedAt: now}, {ID: "new", StartedAt: now.Add(time.Minute)}},
		Context:  []Trigger{{ID: "input", CreatedAt: now, Delivery: DeliveryAtStart}},
		Deliveries: []Delivery{
			{TriggerID: "input", AttemptID: "old", State: DeliveryRejected, CreatedAt: now, UpdatedAt: now},
			{TriggerID: "input", AttemptID: "new", State: DeliveryAtStart, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)},
		},
	}
	if stage := task.Stage(); stage != "working" {
		t.Fatalf("old rejected delivery governed the new attempt: %s", stage)
	}
	task.Deliveries[1].State = DeliveryUnknown
	if stage := task.Stage(); stage != "input undelivered" {
		t.Fatalf("current undelivered state was missed: %s", stage)
	}
}

func TestOpeningStateSettlesPendingDeliveryRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	current := &s.data.Tasks[0]
	current.Attempts = []Attempt{{ID: "attempt-1", Agent: "pi", Status: Running, StartedAt: time.Now().UTC()}}
	current.Context = []Trigger{{ID: "input", Text: "context", CreatedAt: time.Now().UTC(), Delivery: DeliveryPending}}
	current.Deliveries = []Delivery{{TriggerID: "input", AttemptID: "attempt-1", State: DeliveryPending, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}
	if err := s.save(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.GetTask(task.ID)
	if len(got.Deliveries) != 1 || got.Deliveries[0].State != DeliveryUnknown {
		t.Fatalf("pending delivery survived reopen: %+v", got.Deliveries)
	}
	if got.Deliveries[0].Error == "" || got.Context[0].Delivery != DeliveryUnknown {
		t.Fatalf("recovery did not report the uncertainty: %+v", got)
	}
}
