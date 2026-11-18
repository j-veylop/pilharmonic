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
	if strings.Join(got, ",") != "read,bash,write,edit,multiedit,grep,rg,glob,find,ls,diagnostics" {
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

func TestGlobAndRgFindWorkspaceFiles(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(workspace, "pkg", "deep"), 0o755))
	must(t, os.WriteFile(filepath.Join(workspace, "pkg", "deep", "main.go"), []byte("package deep\n\nfunc Run() {}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello world\n"), 0o644))

	globbed := runTool(t, workspace, "glob", `{"pattern":"**/*.go"}`)
	if globbed.IsError || !strings.Contains(globbed.Content, "pkg/deep/main.go") {
		t.Fatalf("glob missed the nested file: %+v", globbed)
	}

	hit := runTool(t, workspace, "rg", `{"pattern":"func Run","glob":"*.go"}`)
	if hit.IsError || !strings.Contains(hit.Content, "pkg/deep/main.go:3:") {
		t.Fatalf("rg missed the match: %+v", hit)
	}
	if miss := runTool(t, workspace, "rg", `{"pattern":"absent-token"}`); miss.IsError || !strings.Contains(miss.Content, "no matches") {
		t.Fatalf("empty rg should be an answer: %+v", miss)
	}
}

func TestMultieditAppliesOrderedEdits(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.txt")
	must(t, os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644))

	result := runTool(t, workspace, "multiedit", `{"edits":[
		{"path":"a.txt","old_text":"alpha","new_text":"ALPHA"},
		{"path":"a.txt","old_text":"gamma","new_text":"GAMMA"}
	]}`)
	if result.IsError {
		t.Fatalf("multiedit: %+v", result)
	}
	data, err := os.ReadFile(path)
	must(t, err)
	if string(data) != "ALPHA\nbeta\nGAMMA\n" {
		t.Fatalf("edits not applied in order: %q", data)
	}
}

func TestEditFallsBackToWhitespaceTolerantMatch(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.go")
	must(t, os.WriteFile(path, []byte("func run() {\n\tif true {\n\t\twork()\n\t}\n}\n"), 0o644))

	result := runTool(t, workspace, "edit", `{"path":"a.go","old_text":"    if true {\n        work()\n    }","new_text":"    if ready {\n        work()\n    }"}`)
	if result.IsError {
		t.Fatalf("tolerant edit: %+v", result)
	}
	data, err := os.ReadFile(path)
	must(t, err)
	if !strings.Contains(string(data), "\tif ready {\n\t\twork()\n\t}") {
		t.Fatalf("edit did not keep file indentation: %q", data)
	}
}

func TestDiagnosticsWithoutGoModuleIsAnAnswer(t *testing.T) {
	result := runTool(t, t.TempDir(), "diagnostics", `{}`)
	if result.IsError || !strings.Contains(result.Content, "no Go module") {
		t.Fatalf("diagnostics outside a module: %+v", result)
	}
}

func TestReadToolLineWindows(t *testing.T) {
	workspace := t.TempDir()
	content := "one\ntwo\nthree\nfour\n"
	writeWorkspaceFile(t, workspace, "lines.txt", content)

	whole := runTool(t, workspace, "read", `{"path":"lines.txt"}`)
	if whole.IsError || whole.Content != content {
		t.Fatalf("whole read = %+v, want verbatim %q", whole, content)
	}

	window := runTool(t, workspace, "read", `{"path":"lines.txt","offset":2,"limit":2}`)
	if window.IsError {
		t.Fatalf("window read: %+v", window)
	}
	want := "two\nthree\n[read: lines 2-3 of 4; continue with offset=4]"
	if window.Content != want {
		t.Fatalf("window read = %q, want %q", window.Content, want)
	}
	if strings.Contains(window.Content, "1:one") {
		t.Fatalf("windowed content was numbered: %q", window.Content)
	}

	past := runTool(t, workspace, "read", `{"path":"lines.txt","offset":9}`)
	if !past.IsError || !strings.Contains(past.Content, "past the end") {
		t.Fatalf("past-the-end read = %+v", past)
	}
}

func TestEditRequiresUniqueMatchUnlessReplaceAll(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "dup.txt", "same\nmiddle\nsame\n")

	rejected := runTool(t, workspace, "edit", `{"path":"dup.txt","old_text":"same","new_text":"different"}`)
	if !rejected.IsError || !strings.Contains(rejected.Content, "appears 2 times") {
		t.Fatalf("non-unique edit = %+v", rejected)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "dup.txt"))
	must(t, err)
	if string(data) != "same\nmiddle\nsame\n" {
		t.Fatalf("rejected edit changed the file: %q", data)
	}

	replaced := runTool(t, workspace, "edit", `{"path":"dup.txt","old_text":"same","new_text":"different","replace_all":true}`)
	if replaced.IsError {
		t.Fatalf("replace_all edit: %+v", replaced)
	}
	data, err = os.ReadFile(filepath.Join(workspace, "dup.txt"))
	must(t, err)
	if string(data) != "different\nmiddle\ndifferent\n" {
		t.Fatalf("replace_all result: %q", data)
	}
}

func TestEditNotFoundNamesClosestLine(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "near.txt", "alpha\n  beta\n")

	result := runTool(t, workspace, "edit", `{"path":"near.txt","old_text":"beta\nmissing","new_text":"x"}`)
	if !result.IsError {
		t.Fatalf("edit without a match succeeded: %+v", result)
	}
	if !strings.Contains(result.Content, "old_text not found") || !strings.Contains(result.Content, "closest match at line 2") {
		t.Fatalf("not-found hint missing: %q", result.Content)
	}
}

func TestMultieditIsAtomicAcrossFiles(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "a.txt", "alpha\n")
	writeWorkspaceFile(t, workspace, "b.txt", "beta\n")

	result := runTool(t, workspace, "multiedit", `{"edits":[
		{"path":"a.txt","old_text":"alpha","new_text":"ALPHA"},
		{"path":"b.txt","old_text":"missing","new_text":"BETA"}
	]}`)
	if !result.IsError {
		t.Fatalf("failing multiedit succeeded: %+v", result)
	}
	for path, want := range map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"} {
		data, err := os.ReadFile(filepath.Join(workspace, path))
		must(t, err)
		if string(data) != want {
			t.Fatalf("%s changed although the batch failed: %q", path, data)
		}
	}
}

func TestBashToolTimeoutIsASoftResult(t *testing.T) {
	started := time.Now()
	result := runTool(t, t.TempDir(), "bash", `{"command":"sleep 5","description":"test the timeout","timeout_ms":50}`)
	if result.IsError {
		t.Fatalf("tool timeout was a hard failure: %+v", result)
	}
	if !strings.Contains(result.Content, "timed out after 50ms") {
		t.Fatalf("timeout not explained: %q", result.Content)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout did not bound the command: %s", elapsed)
	}
}
