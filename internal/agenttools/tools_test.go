package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"

	"pilharmonic/internal/skills"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func runTool(t *testing.T, workspace, name, input string) fantasy.ToolResponse {
	t.Helper()
	for _, tool := range Tools(workspace, []string{name}, nil) {
		if tool.Info().Name != name {
			continue
		}
		response, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "call-1", Name: name, Input: input})
		if err != nil {
			t.Fatalf("%s returned an error: %v", name, err)
		}
		return response
	}
	t.Fatalf("tool %q is not exposed", name)
	return fantasy.ToolResponse{}
}

func TestToolsCoverThePalette(t *testing.T) {
	tools := Tools(t.TempDir(), nil, nil)
	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		got = append(got, tool.Info().Name)
	}
	if strings.Join(got, ",") != "read,bash,write,edit,grep,find,ls" {
		t.Fatalf("unexpected tools: %v", got)
	}
	subset := Tools(t.TempDir(), []string{"read", "unknown-tool"}, nil)
	if len(subset) != 1 || subset[0].Info().Name != "read" {
		t.Fatalf("palette filtering failed: %v", subset)
	}
}

func TestToolsExposeInstalledSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "verify")
	must(t, os.MkdirAll(skillDir, 0o755))
	must(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: verify\ndescription: Verify a change.\n---\nRun the checks.\n"), 0o644))
	catalog, err := skills.Load(dir)
	must(t, err)

	tools := ToolsWithSkills(t.TempDir(), []string{"read"}, nil, catalog)
	if len(tools) != 2 || tools[1].Info().Name != "skill" {
		t.Fatalf("skill tool missing from %v", tools)
	}
	response, err := tools[1].Run(context.Background(), fantasy.ToolCall{ID: "call-1", Name: "skill", Input: `{"name":"verify"}`})
	must(t, err)
	if response.IsError || !strings.Contains(response.Content, "Run the checks.") {
		t.Fatalf("skill response: %+v", response)
	}
	missing, err := tools[1].Run(context.Background(), fantasy.ToolCall{ID: "call-2", Name: "skill", Input: `{"name":"unknown"}`})
	must(t, err)
	if !missing.IsError {
		t.Fatalf("unknown skill did not fail: %+v", missing)
	}
	if len(Tools(t.TempDir(), nil, nil)) != len(Names()) {
		t.Fatal("empty catalog changed the default palette")
	}
}

func TestToolsFileOperations(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("alpha beta\n"), 0o644))

	read := runTool(t, workspace, "read", `{"path":"a.txt"}`)
	if read.IsError || !strings.Contains(read.Content, "alpha beta") {
		t.Fatalf("read: %+v", read)
	}
	if missing := runTool(t, workspace, "read", `{"path":"missing.txt"}`); !missing.IsError {
		t.Fatalf("read of a missing file should report an error: %+v", missing)
	}
	if escape := runTool(t, workspace, "read", `{"path":"../outside.txt"}`); !escape.IsError || !strings.Contains(escape.Content, "escapes the workspace") {
		t.Fatalf("traversal not refused: %+v", escape)
	}
	if absolute := runTool(t, workspace, "read", `{"path":"/etc/hostname"}`); !absolute.IsError {
		t.Fatalf("absolute path not refused: %+v", absolute)
	}

	write := runTool(t, workspace, "write", `{"path":"sub/b.txt","content":"hello"}`)
	if write.IsError {
		t.Fatalf("write: %+v", write)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "sub", "b.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("write did not land in the workspace: %q %v", data, err)
	}

	edit := runTool(t, workspace, "edit", `{"path":"a.txt","old_text":"beta","new_text":"gamma"}`)
	if edit.IsError {
		t.Fatalf("edit: %+v", edit)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "a.txt")); string(data) != "alpha gamma\n" {
		t.Fatalf("edit result: %q", data)
	}
	if miss := runTool(t, workspace, "edit", `{"path":"a.txt","old_text":"absent","new_text":"x"}`); !miss.IsError {
		t.Fatalf("edit without a match should report an error: %+v", miss)
	}
}

func TestToolsCommands(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("needle in a haystack\n"), 0o644))

	if got := runTool(t, workspace, "grep", `{"pattern":"needle","path":"notes.md"}`); got.IsError || !strings.Contains(got.Content, "notes.md:1:needle") {
		t.Fatalf("grep: %+v", got)
	}
	if got := runTool(t, workspace, "find", `{"name":"*.md"}`); got.IsError || !strings.Contains(got.Content, "notes.md") {
		t.Fatalf("find: %+v", got)
	}
	if got := runTool(t, workspace, "ls", `{}`); got.IsError || !strings.Contains(got.Content, "notes.md") {
		t.Fatalf("ls: %+v", got)
	}
	if got := runTool(t, workspace, "bash", `{"command":"printf 'from bash'"}`); got.IsError || !strings.Contains(got.Content, "from bash") {
		t.Fatalf("bash: %+v", got)
	}
	// The bash tool runs in the workspace, not in the orchestrator's directory.
	if got := runTool(t, workspace, "bash", `{"command":"cat notes.md"}`); got.IsError || !strings.Contains(got.Content, "needle in a haystack") {
		t.Fatalf("bash working directory: %+v", got)
	}
}

func TestGrepToolSearchesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "notes.md", "first line\nneedle in a haystack\n")
	writeWorkspaceFile(t, workspace, "pkg/deep.go", "package pkg // needle\n")
	writeWorkspaceFile(t, workspace, ".git/config", "needle in git metadata\n")

	// No path: the whole workspace is searched recursively, and the default
	// has to find the needle instead of waiting on stdin.
	got := runTool(t, workspace, "grep", `{"pattern":"needle"}`)
	if got.IsError {
		t.Fatalf("grep without a path failed: %+v", got)
	}
	for _, want := range []string{"notes.md:2:needle in a haystack", "pkg/deep.go:1:package pkg // needle"} {
		if !strings.Contains(got.Content, want) {
			t.Fatalf("grep output missing %q: %q", want, got.Content)
		}
	}
	if strings.Contains(got.Content, ".git/config") {
		t.Fatalf("grep searched Git metadata: %q", got.Content)
	}

	// A directory path recurses into it and stays inside it.
	got = runTool(t, workspace, "grep", `{"pattern":"needle","path":"pkg"}`)
	if got.IsError || !strings.Contains(got.Content, "deep.go:1:package pkg // needle") {
		t.Fatalf("grep of a directory: %+v", got)
	}
	if strings.Contains(got.Content, "notes.md") {
		t.Fatalf("grep of a directory left that directory: %q", got.Content)
	}

	// A file path names the file in each hit.
	got = runTool(t, workspace, "grep", `{"pattern":"needle","path":"notes.md"}`)
	if got.IsError || !strings.Contains(got.Content, "notes.md:2:needle in a haystack") {
		t.Fatalf("grep of a file: %+v", got)
	}

	// A search without matches is information for the model, not a failure.
	got = runTool(t, workspace, "grep", `{"pattern":"absent"}`)
	if got.IsError {
		t.Fatalf("grep without matches reported an error: %+v", got)
	}
	if !strings.Contains(got.Content, "no matches") || !strings.Contains(got.Content, "absent") {
		t.Fatalf("grep without matches is not explained: %q", got.Content)
	}

	// A bad path is still a real failure, not an empty result.
	if got := runTool(t, workspace, "grep", `{"pattern":"needle","path":"missing"}`); !got.IsError {
		t.Fatalf("grep of a missing path should report an error: %+v", got)
	}

	// An invalid pattern is still a real failure worth reporting.
	if got := runTool(t, workspace, "grep", `{"pattern":"["}`); !got.IsError {
		t.Fatalf("invalid pattern should report an error: %+v", got)
	}
	// An empty pattern is refused instead of matching every line.
	if got := runTool(t, workspace, "grep", `{"pattern":"  "}`); !got.IsError || !strings.Contains(got.Content, "pattern is required") {
		t.Fatalf("empty pattern not refused: %+v", got)
	}
	// A path outside the workspace is refused, like the read tool.
	if got := runTool(t, workspace, "grep", `{"pattern":"needle","path":"../"}`); !got.IsError || !strings.Contains(got.Content, "escapes the workspace") {
		t.Fatalf("traversal not refused: %+v", got)
	}
}

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	path := filepath.Join(workspace, rel)
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestBashToolHonorsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	var bash fantasy.AgentTool
	for _, tool := range Tools(t.TempDir(), []string{"bash"}, nil) {
		bash = tool
	}
	if bash == nil {
		t.Fatal("bash tool missing")
	}
	response, err := bash.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"exec sleep 30"}`})
	if err != nil {
		t.Fatalf("bash returned a hard error: %v", err)
	}
	if !response.IsError || !strings.Contains(response.Content, "context deadline exceeded") {
		t.Fatalf("cancellation not reported: %+v", response)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cancellation ignored: %s", elapsed)
	}
}
