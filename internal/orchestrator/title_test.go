package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
)

func TestTitleTextNormalizesModelOutput(t *testing.T) {
	long := strings.Repeat("word ", 30)
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "Add task numbering", want: "Add task numbering"},
		{name: "surrounding space", in: "  Add task numbering\n", want: "Add task numbering"},
		{name: "label", in: "Title: Fix the parser.", want: "Fix the parser"},
		{name: "task title label", in: "TASK TITLE: Fix the parser", want: "Fix the parser"},
		{name: "quotes and backticks", in: "`Make the quit dialog prettier`", want: "Make the quit dialog prettier"},
		{name: "wrapped", in: "\n\n  Improve titles \nignored second line", want: "Improve titles"},
		{name: "controls removed", in: "Add\x00 titles\x1b", want: "Add titles"},
		{name: "only punctuation", in: "\"`*_ .", want: ""},
		{name: "long", in: long, want: strings.TrimSpace(string([]rune(strings.Join(strings.Fields(long), " "))[:maxTitleRunes-3])) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleText(tc.in); got != tc.want {
				t.Fatalf("titleText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := titleText(long); len([]rune(got)) != maxTitleRunes || !strings.HasSuffix(got, "...") {
		t.Fatalf("long title = %q (%d runes)", got, len([]rune(got)))
	}
}

func TestDerivedTitleDropsExecutorMention(t *testing.T) {
	if got := DerivedTitle("@reviewer review the parser"); got != "review the parser" {
		t.Fatalf("DerivedTitle kept the mention: %q", got)
	}
	if got := DerivedTitle("  fix   the parser "); got != "fix the parser" {
		t.Fatalf("DerivedTitle = %q", got)
	}
}

func TestGenerateTitleUsesPromptAndParsesResponse(t *testing.T) {
	model := &titleFakeModel{reply: "Title: Ship the parser fix."}
	factory := func(_ context.Context, selector string) (fantasy.LanguageModel, error) {
		if selector != "hyper/prism-flash" {
			t.Fatalf("selector = %q", selector)
		}
		return model, nil
	}
	got, err := generateTitleWith(context.Background(), factory, "hyper/prism-flash", strings.Repeat("fix the parser ", 1000))
	must(t, err)
	if got != "Ship the parser fix" {
		t.Fatalf("generated title = %q", got)
	}
	if model.call == nil {
		t.Fatal("title model was not called")
	}
	prompt := model.call.Prompt
	if len(prompt) != 2 || prompt[0].Role != fantasy.MessageRoleSystem || prompt[1].Role != fantasy.MessageRoleUser {
		t.Fatalf("unexpected title prompt: %+v", prompt)
	}
	if !strings.Contains(prompt[0].Content[0].(fantasy.TextPart).Text, "short title") {
		t.Fatalf("title instruction missing: %+v", prompt[0])
	}
	if mandate := prompt[1].Content[0].(fantasy.TextPart).Text; len([]rune(mandate)) > maxTitleMandateRunes {
		t.Fatalf("mandate not bounded: %d runes", len([]rune(mandate)))
	}
	if model.call.MaxOutputTokens == nil || *model.call.MaxOutputTokens <= 0 {
		t.Fatal("title call is unbounded")
	}
}

func TestGenerateTitleKeepsDerivedTitleOnFailure(t *testing.T) {
	factory := func(context.Context, string) (fantasy.LanguageModel, error) {
		return nil, errors.New("no credential")
	}
	if got, err := generateTitleWith(context.Background(), factory, "hyper/prism", "fix it"); err == nil || got != "" {
		t.Fatalf("generateTitleWith = %q, %v", got, err)
	}
}

type titleFakeModel struct {
	reply string
	call  *fantasy.Call
}

func (m *titleFakeModel) Generate(_ context.Context, call fantasy.Call) (*fantasy.Response, error) {
	m.call = &call
	return &fantasy.Response{Content: fantasy.ResponseContent{fantasy.TextContent{Text: m.reply}}}, nil
}

func (m *titleFakeModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, errors.New("no stream support")
}

func (m *titleFakeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("no object support")
}

func (m *titleFakeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("no object support")
}

func (m *titleFakeModel) Provider() string { return "fake" }
func (m *titleFakeModel) Model() string    { return "fake/model" }

func TestTitleModelFallsBackToTaskModel(t *testing.T) {
	s := newTestService(t)
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	if got := s.titleModelLocked(DefaultRouteAgent, nil); got != "hyper/prism" {
		t.Fatalf("title model = %q, want the task model", got)
	}
	must(t, s.SetModel(ModelClassSmall, "hyper/prism-flash"))
	if got := s.titleModelLocked(DefaultRouteAgent, nil); got != "hyper/prism-flash" {
		t.Fatalf("title model = %q, want the small class", got)
	}
}

func TestSubmitRequestGeneratesTitle(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	must(t, s.SetModel(ModelClassSmall, "hyper/prism-flash"))
	models := make(chan string, 1)
	s.SetTitleGenerator(func(_ context.Context, model, mandate string) (string, error) {
		if !strings.Contains(mandate, "Fix the login bug") {
			t.Errorf("generator received mandate %q", mandate)
		}
		models <- model
		return "Fix login validation", nil
	})

	task := mustGet(s.SubmitRequest("Fix the login bug\nPlease add coverage.", t.TempDir()))(t)
	if task.Title != "Fix the login bug" || task.TitleGenerated {
		t.Fatalf("acceptance did not keep the derived title: %+v", task)
	}
	if model := <-models; model != "hyper/prism-flash" {
		t.Fatalf("title model = %q, want the configured small model", model)
	}
	done := awaitTitle(t, s, task.ID, "Fix login validation")
	if !done.TitleGenerated || done.Prompt != task.Prompt {
		t.Fatalf("generated title replaced more than the display title: %+v", done)
	}
	if done.Decision.InputDigest != task.Decision.InputDigest || done.Decision.Outcome != OutcomeAccept {
		t.Fatalf("title generation changed the acceptance decision: %+v", done.Decision)
	}
	titled := false
	for _, event := range done.Events {
		if event.Kind == "titled" && strings.Contains(event.Message, "Fix login validation") {
			titled = true
		}
	}
	if !titled {
		t.Fatalf("generated title was not recorded in history: %+v", done.Events)
	}
}

func TestExplicitTitleIsNotGenerated(t *testing.T) {
	s := newTestService(t)
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	called := make(chan struct{}, 1)
	s.SetTitleGenerator(func(context.Context, string, string) (string, error) {
		called <- struct{}{}
		return "generated", nil
	})
	task := createTask(t, s, "Chosen title", "do work", t.TempDir())
	select {
	case <-called:
		t.Fatal("explicit title was sent to the title model")
	case <-time.After(50 * time.Millisecond):
	}
	if task.Title != "Chosen title" || task.TitleGenerated {
		t.Fatalf("explicit title changed: %+v", task)
	}
}

func TestTitleGenerationFailureKeepsDerivedTitle(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	called := make(chan struct{}, 1)
	s.SetTitleGenerator(func(context.Context, string, string) (string, error) {
		called <- struct{}{}
		return "", errors.New("provider unavailable")
	})
	task := mustGet(s.SubmitRequest("Fix the parser", t.TempDir()))(t)
	<-called
	await(t, s, task.ID, Completed)
	current, _ := s.GetTask(task.ID)
	if current.Title != "Fix the parser" || current.TitleGenerated {
		t.Fatalf("failed generation changed the title: %+v", current)
	}
}

func TestTitleGenerationNeedsConfiguredModel(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	called := make(chan struct{}, 1)
	s.SetTitleGenerator(func(context.Context, string, string) (string, error) {
		called <- struct{}{}
		return "generated", nil
	})
	task := mustGet(s.SubmitRequest("Fix the parser", t.TempDir()))(t)
	await(t, s, task.ID, Completed)
	select {
	case <-called:
		t.Fatal("title model ran without a configured model")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGeneratedTitlePersistsAndDoesNotBlockShutdown(t *testing.T) {
	dir := t.TempDir()
	statePath := dir + "/state.json"
	s := openTestService(t, statePath)
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	s.SetTitleGenerator(func(context.Context, string, string) (string, error) {
		return "Persisted title", nil
	})
	task := mustGet(s.SubmitRequest("Fix the parser", dir))(t)
	awaitTitle(t, s, task.ID, "Persisted title")
	must(t, s.Shutdown())

	reopened := openTestService(t, statePath)
	persisted, ok := reopened.GetTask(task.ID)
	if !ok || persisted.Title != "Persisted title" || !persisted.TitleGenerated {
		t.Fatalf("generated title not persisted: %+v", persisted)
	}
}

func TestSubagentWithoutTitleGeneratesOne(t *testing.T) {
	s := newTestService(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.RegisterRunner(DefaultRouteAgent, runnerFunc(func(_ context.Context, task Task, out io.Writer) error {
		if task.ParentID == "" {
			started <- struct{}{}
			<-release
			return nil
		}
		_, err := io.WriteString(out, "child result")
		return err
	}))
	must(t, s.SetModel(ModelClassBig, "hyper/prism"))
	s.SetTitleGenerator(func(_ context.Context, _ string, mandate string) (string, error) {
		if !strings.Contains(mandate, "say hi") {
			t.Errorf("generator received mandate %q", mandate)
		}
		return "Say hello", nil
	})
	parent := createTask(t, s, "parent", "parent mandate", t.TempDir())
	must(t, s.Start(context.Background(), parent.ID))
	<-started
	child := mustGet(s.SpawnSubagent(parent.ID, "", "say hi to the user", ""))(t)
	done := awaitTitle(t, s, child.ID, "Say hello")
	if !done.TitleGenerated || done.Title != "Say hello" {
		t.Fatalf("child title not generated: %+v", done)
	}
	close(release)
	await(t, s, parent.ID, Completed)
}

// awaitTitle waits for best-effort title generation to land.
func awaitTitle(t *testing.T, s *Service, id, want string) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := s.GetTask(id)
		if task.Title == want {
			return task
		}
		time.Sleep(time.Millisecond * 5)
	}
	task, _ := s.GetTask(id)
	t.Fatalf("title = %q, want %q", task.Title, want)
	return Task{}
}
