// Package agenttools implements the workspace tools an in-process agent may
// call. Workspace tools are confined to the task workspace and return bounded,
// sanitized text; none composes shell text from task input.
package agenttools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"

	"github.com/j-veylop/pilharmonic/internal/skills"
)

const (
	// maxToolReadBytes bounds a single file read so a huge file cannot blow up
	// the model's context (or the persisted tool activity).
	maxToolReadBytes = 256 << 10
	// maxReadScanBytes bounds how much of a file a windowed read loads before
	// slicing lines. A larger file is served from its first 8 MiB.
	maxReadScanBytes = 8 << 20
	// maxToolOutputBytes bounds captured command output. The shared capped
	// buffer keeps half the head and half the tail, because a failing command
	// usually explains itself in its last lines.
	maxToolOutputBytes = 64 << 10
	// defaultBashTimeout bounds one bash call, so a hung command cannot hold an
	// attempt open forever.
	defaultBashTimeout = 120 * time.Second
	// maxDiagnosticNote bounds the diagnostics appended to an edit result.
	maxDiagnosticNote = 4 << 10
)

// names is the complete palette, in display order. A profile or a role's
// default palette narrows it; nothing outside the list is available.
var names = []string{"read", "bash", "write", "edit", "multiedit", "grep", "rg", "glob", "find", "ls", "diagnostics"}

// Names returns the complete tool palette.
func Names() []string { return append([]string(nil), names...) }

// Tools builds the agent-side tool set for one attempt. The palette selects a
// subset of Names; unknown names are ignored so a profile that mentions a tool
// this package lacks still runs with the rest.
// Every tool is confined to workspace: file paths are resolved inside it and
// commands run with it as their working directory.
func Tools(workspace string, palette []string, env []string) []fantasy.AgentTool {
	return ToolsWithSkills(workspace, palette, env, skills.Catalog{})
}

// ToolsWithSkills builds the workspace tools and, when the catalog is not
// empty, the skill tool. Skills are local operator instructions, not workspace
// authority, so the skill tool is available regardless of the workspace
// palette.
func ToolsWithSkills(workspace string, palette []string, env []string, catalog skills.Catalog) []fantasy.AgentTool {
	return ToolsWithOptions(workspace, palette, env, catalog, Options{})
}

// Options carries host-provided behavior into the tool set. Diagnostics, when
// set, runs the host's enabled language checks instead of the built-in Go
// fallback.
type Options struct {
	Diagnostics func(ctx context.Context, workspace string, files []string) string
}

// ToolsWithOptions is ToolsWithSkills plus host hooks.
func ToolsWithOptions(workspace string, palette []string, env []string, catalog skills.Catalog, options Options) []fantasy.AgentTool {
	selected := make(map[string]bool, len(palette))
	if len(palette) == 0 {
		palette = names
	}
	for _, name := range palette {
		selected[name] = true
	}
	tools := make([]fantasy.AgentTool, 0, len(palette))
	for _, name := range names {
		if !selected[name] {
			continue
		}
		switch name {
		case "read":
			tools = append(tools, readTool(workspace))
		case "bash":
			tools = append(tools, bashTool(workspace, env))
		case "write":
			tools = append(tools, writeTool(workspace, options))
		case "edit":
			tools = append(tools, editTool(workspace, options))
		case "multiedit":
			tools = append(tools, multiEditTool(workspace, options))
		case "grep":
			tools = append(tools, grepTool(workspace, env))
		case "rg":
			tools = append(tools, rgTool(workspace))
		case "glob":
			tools = append(tools, globTool(workspace))
		case "find":
			tools = append(tools, findTool(workspace, env))
		case "ls":
			tools = append(tools, lsTool(workspace, env))
		case "diagnostics":
			tools = append(tools, diagnosticsTool(workspace, options.Diagnostics))
		}
	}
	if catalog.Len() > 0 {
		tools = append(tools, skillTool(catalog))
	}
	return tools
}

// workspacePath resolves a model-supplied path inside the workspace. Absolute
// paths and parent traversal are refused so a tool cannot read or write outside
// the workspace the orchestrator prepared for the task.
func workspacePath(workspace, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be relative to the workspace", path)
	}
	full := filepath.Join(workspace, path)
	rel, err := filepath.Rel(workspace, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", path)
	}
	return full, nil
}

// runWorkspaceCommand runs one program with explicit arguments inside the
// workspace. Only the bash tool passes a command string; every other caller
// supplies fixed arguments, so model input is never interpolated into shell
// text here.
func runWorkspaceCommand(ctx context.Context, workspace string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workspace
	if len(env) > 0 {
		cmd.Env = env
	}
	output := &cappedBuffer{max: maxToolOutputBytes / 2}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	return Sanitize(output.String()), err
}

// commandResponse renders a bounded command result for the model. A failing
// command is a message rather than a hard error: the model can read the output
// and try another approach, exactly like a shell user would.
func commandResponse(name, text string, err error) fantasy.ToolResponse {
	text = strings.TrimSpace(text)
	if err != nil {
		if text == "" {
			text = fmt.Sprintf("%s failed: %v", name, err)
		}
		return fantasy.NewTextErrorResponse(text)
	}
	if text == "" {
		return fantasy.NewTextResponse(name + ": no output")
	}
	return fantasy.NewTextResponse(text)
}

type readToolInput struct {
	Path   string `json:"path" description:"Workspace-relative path of the file to read"`
	Offset int    `json:"offset,omitempty" description:"First line to return, 1-based; omit to start at the top of the file"`
	Limit  int    `json:"limit,omitempty" description:"Maximum number of lines to return; omit for the rest of the file within the size budget"`
}

type bashToolInput struct {
	Command     string `json:"command" description:"Shell command to run with the workspace as its working directory"`
	Description string `json:"description" description:"One line saying what the command does and why it is being run"`
	TimeoutMS   int    `json:"timeout_ms,omitempty" description:"Abort after this many milliseconds; defaults to 120000"`
}

type writeToolInput struct {
	Path    string `json:"path" description:"Workspace-relative path of the file to write"`
	Content string `json:"content" description:"Complete new contents of the file"`
}

type editToolInput struct {
	Path       string `json:"path" description:"Workspace-relative path of the file to edit"`
	OldText    string `json:"old_text" description:"Exact text to replace; it must occur exactly once unless replace_all is set"`
	NewText    string `json:"new_text" description:"Replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" description:"Replace every occurrence instead of requiring exactly one (default false)"`
}

type grepToolInput struct {
	Pattern string `json:"pattern" description:"Regular expression to search for, not a file glob"`
	Path    string `json:"path,omitempty" description:"Optional workspace-relative file or directory to search; defaults to the whole workspace"`
}

type findToolInput struct {
	Name string `json:"name" description:"File name or glob pattern to locate, for example *service.go"`
}

type lsToolInput struct {
	Path string `json:"path,omitempty" description:"Optional workspace-relative directory to list; defaults to the workspace root"`
}

type skillToolInput struct {
	Name string `json:"name" description:"Name of the installed skill to load"`
}

func readTool(workspace string) fantasy.AgentTool {
	return fantasy.NewAgentTool("read", "Read a file from the workspace. Returns the whole file, or the line window named by offset and limit. Content is returned verbatim, without line numbers; grep and rg report line numbers. A window that does not reach the end of the file says how to continue.",
		func(_ context.Context, in readToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			path, err := workspacePath(workspace, in.Path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			file, err := os.Open(path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			defer func() { _ = file.Close() }()
			data, err := io.ReadAll(io.LimitReader(file, maxReadScanBytes+1))
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			oversizedFile := len(data) > maxReadScanBytes
			if oversizedFile {
				data = data[:maxReadScanBytes]
			}
			// File contents are returned verbatim: they are the model's own
			// working data (an edit must match them exactly), and this text
			// travels through the provider API, never through the terminal.
			text, err := readWindow(string(data), in.Offset, in.Limit)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if oversizedFile {
				text += "\n[read stopped at 8 MiB of a larger file]"
			}
			return fantasy.NewTextResponse(text), nil
		})
}

// readWindow returns the requested line range of text. Content is never
// numbered, so an edit can still match it byte for byte; a marker names the
// range when it is not the whole file.
func readWindow(text string, offset, limit int) (string, error) {
	if offset < 0 || limit < 0 {
		return "", errors.New("offset and limit must not be negative")
	}
	var starts []int
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	total := len(starts) + 1
	if len(text) == 0 || (len(starts) > 0 && starts[len(starts)-1] == len(text)) {
		total--
	}
	if total == 0 {
		return "", nil
	}
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return "", fmt.Errorf("offset %d is past the end of a %d-line file", offset, total)
	}
	start := 0
	if offset > 1 {
		start = starts[offset-2]
	}
	end := len(text)
	if limit > 0 {
		last := offset + limit - 1
		if last < total {
			end = starts[last-1]
		}
	}
	if end > start+maxToolReadBytes {
		end = start + maxToolReadBytes
	}
	body := text[start:end]
	if end >= len(text) {
		return body, nil
	}
	lastLine := offset + strings.Count(body, "\n")
	if strings.HasSuffix(body, "\n") {
		lastLine--
	}
	note := fmt.Sprintf("[read: lines %d-%d of %d; continue with offset=%d]", offset, lastLine, total, lastLine+1)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + note, nil
}

func bashTool(workspace string, env []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("bash", "Run a shell command with the workspace as its working directory and return its combined output. The command is killed at timeout_ms, and its output is bounded.",
		func(ctx context.Context, in bashToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			timeout := defaultBashTimeout
			if in.TimeoutMS > 0 {
				timeout = time.Duration(in.TimeoutMS) * time.Millisecond
			}
			parent := ctx
			ctx, cancel := context.WithTimeout(parent, timeout)
			defer cancel()
			// This is the adapter's only shell path:
			// the command is the model's own, and it runs in the workspace the
			// orchestrator prepared. It is never composed from task or trigger
			// text by the orchestrator itself.
			text, err := runWorkspaceCommand(ctx, workspace, env, "bash", "-c", in.Command)
			if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				// This call's own budget ran out. Report it as a result the
				// model can react to; a canceled attempt still fails below.
				message := fmt.Sprintf("bash: timed out after %s", timeout)
				if partial := strings.TrimSpace(text); partial != "" {
					message += "\n" + partial
				}
				return fantasy.NewTextResponse(message), nil
			}
			return commandResponse("bash", text, err), nil
		})
}

func writeTool(workspace string, options Options) fantasy.AgentTool {
	return fantasy.NewAgentTool("write", "Write a file in the workspace, replacing it if it exists. Diagnostics for the written file are appended to the result.",
		func(ctx context.Context, in writeToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			path, err := workspacePath(workspace, in.Path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			message := fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path)
			return fantasy.NewTextResponse(message + diagnosticNote(ctx, options, workspace, []string{in.Path})), nil
		})
}

func editTool(workspace string, options Options) fantasy.AgentTool {
	return fantasy.NewAgentTool("edit", "Replace text in a workspace file. The text must occur exactly once unless replace_all is set; an indentation or trailing-whitespace mismatch falls back to a whitespace-tolerant match. Diagnostics for the changed file are appended to the result.",
		func(ctx context.Context, in editToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			how, count, err := applyEdit(workspace, in.Path, in.OldText, in.NewText, in.ReplaceAll)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			message := fmt.Sprintf("replaced %d occurrence(s) in %s (%s match)", count, in.Path, how)
			if how == "whitespace-tolerant" {
				message += "\nThe text matched only after ignoring whitespace; check the result."
			}
			return fantasy.NewTextResponse(message + diagnosticNote(ctx, options, workspace, []string{in.Path})), nil
		})
}

// applyEdit replaces oldText with newText in one file. It reports how the text
// matched and how many occurrences changed.
func applyEdit(workspace, path, oldText, newText string, replaceAll bool) (string, int, error) {
	full, err := workspacePath(workspace, path)
	if err != nil {
		return "", 0, err
	}
	if oldText == "" {
		return "", 0, errors.New("old_text is required")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", 0, err
	}
	updated, how, count, err := replaceText(string(data), path, oldText, newText, replaceAll)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", 0, err
	}
	return how, count, nil
}

// replaceText replaces oldText in content. A non-unique match is an error
// unless replaceAll is set, so an edit cannot silently rewrite the wrong site.
func replaceText(content, path, oldText, newText string, replaceAll bool) (string, string, int, error) {
	if occurrences := strings.Count(content, oldText); occurrences > 0 {
		if occurrences > 1 && !replaceAll {
			return "", "", 0, fmt.Errorf("old_text appears %d times in %s; include more surrounding text to make it unique, or set replace_all", occurrences, path)
		}
		if replaceAll {
			return strings.ReplaceAll(content, oldText, newText), "exact", occurrences, nil
		}
		return strings.Replace(content, oldText, newText, 1), "exact", 1, nil
	}
	if next, ok := replaceWithWhitespaceTolerance(content, oldText, newText); ok {
		return next, "whitespace-tolerant", 1, nil
	}
	return "", "", 0, notFoundEdit(content, path, oldText)
}

// notFoundEdit explains a failed match with the closest line it can find.
func notFoundEdit(content, path, oldText string) error {
	message := "old_text not found in " + path
	first := strings.TrimSpace(strings.SplitN(oldText, "\n", 2)[0])
	if first == "" {
		return errors.New(message)
	}
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == first {
			message += fmt.Sprintf(" (closest match at line %d: whitespace differs)", i+1)
			break
		}
	}
	return errors.New(message)
}

// diagnosticNote runs the host's language diagnostics for the files just
// changed, so a broken edit is visible in the same turn.
func diagnosticNote(ctx context.Context, options Options, workspace string, paths []string) string {
	if options.Diagnostics == nil || len(paths) == 0 {
		return ""
	}
	text := strings.TrimSpace(options.Diagnostics(ctx, workspace, paths))
	if text == "" {
		return ""
	}
	if len(text) > maxDiagnosticNote {
		cut := maxDiagnosticNote
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n[truncated]"
	}
	return "\n\nDiagnostics for the changed files:\n" + text
}

func grepTool(workspace string, env []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("grep", "Search workspace files recursively for a regular expression and return matching lines as file:line:text. Git metadata is skipped.",
		func(ctx context.Context, in grepToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			pattern := strings.TrimSpace(in.Pattern)
			if pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}
			search := "."
			if path := strings.TrimSpace(in.Path); path != "" {
				if _, err := workspacePath(workspace, path); err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				search = path
			}
			// -r walks directories (a directory operand is otherwise an error)
			// and -H keeps the file name in front of every hit, which a single
			// file operand would otherwise drop. -n numbers lines, -I skips
			// binaries, and -- keeps a pattern that begins with a dash a
			// pattern rather than an option.
			args := []string{"-rnHI", "--exclude-dir=.git", "--", pattern, search}
			text, err := runWorkspaceCommand(ctx, workspace, env, "grep", args...)
			if isGrepNoMatch(err) {
				// grep exits 1 to mean "selected no lines". Reporting that as a
				// failure made an ordinary empty search look like a broken tool.
				return fantasy.NewTextResponse(fmt.Sprintf("grep: no matches for %q in %s",
					Sanitize(pattern), Sanitize(search))), nil
			}
			return commandResponse("grep", text, err), nil
		})
}

// isGrepNoMatch reports whether grep exited with status 1, which means the
// search ran but found no matching lines. Any other failure (status 2, a
// missing grep binary, cancellation) is a real error.
func isGrepNoMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func findTool(workspace string, env []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("find", "Find workspace entries by name or glob pattern.",
		func(ctx context.Context, in findToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			name := strings.TrimSpace(in.Name)
			if name == "" {
				return fantasy.NewTextErrorResponse("name is required"), nil
			}
			text, err := runWorkspaceCommand(ctx, workspace, env, "find", ".", "-name", name)
			return commandResponse("find", text, err), nil
		})
}

func lsTool(workspace string, env []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("ls", "List a workspace directory.",
		func(ctx context.Context, in lsToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			path := strings.TrimSpace(in.Path)
			if path == "" {
				path = "."
			}
			if _, err := workspacePath(workspace, path); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			text, err := runWorkspaceCommand(ctx, workspace, env, "ls", "-la", path)
			return commandResponse("ls", text, err), nil
		})
}

func skillTool(catalog skills.Catalog) fantasy.AgentTool {
	return fantasy.NewAgentTool("skill", "Load the full instructions of an installed skill by name.",
		func(_ context.Context, in skillToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			skill, ok := catalog.Lookup(strings.TrimSpace(in.Name))
			if !ok {
				return fantasy.NewTextErrorResponse("unknown skill: " + strings.TrimSpace(in.Name)), nil
			}
			return fantasy.NewTextResponse("# Skill: " + skill.Name + "\n\n" + skill.Body), nil
		})
}

// Sanitize strips terminal control bytes from agent-supplied text.
// Newlines and tabs are kept; C0/C1 controls, DEL and ESC are removed so output
// cannot rewrite the terminal.
func Sanitize(text string) string {
	var clean strings.Builder
	clean.Grow(len(text))
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			// An invalid byte is kept unless it falls in a control range.
			if c := text[i]; c >= 0x20 && (c < 0x7f || c > 0x9f) {
				clean.WriteByte(c)
			}
		} else if r == '\n' || r == '\t' || r >= 0x20 && (r < 0x7f || r > 0x9f) {
			clean.WriteRune(r)
		}
		i += size
	}
	return clean.String()
}

// cappedBuffer bounds captured command output while keeping both its beginning
// and its end, where the useful diagnostic often is. os/exec writes from its
// own goroutine, hence the mutex.
type cappedBuffer struct {
	mu         sync.Mutex
	head, tail []byte
	max        int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	if len(b.head) < b.max {
		take := min(b.max-len(b.head), len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > b.max {
			b.tail = b.tail[len(b.tail)-b.max:]
		}
	}
	return written, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.tail) == 0 {
		return string(b.head)
	}
	return string(b.head) + "\n[...]\n" + string(b.tail)
}
