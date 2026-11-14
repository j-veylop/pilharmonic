// Package agenttools implements the workspace tools an in-process agent may
// call. Every tool is confined to the task workspace and returns bounded,
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
	"unicode/utf8"

	"charm.land/fantasy"
)

const (
	// maxToolReadBytes bounds a single file read so a huge file cannot blow up
	// the model's context (or the persisted tool activity).
	maxToolReadBytes = 256 << 10
	// maxToolOutputBytes bounds captured command output. The shared capped
	// buffer keeps half the head and half the tail, because a failing command
	// usually explains itself in its last lines.
	maxToolOutputBytes = 64 << 10
)

// names is the complete palette, in display order. A profile or a role's
// default palette narrows it; nothing outside the list is available.
var names = []string{"read", "bash", "write", "edit", "grep", "find", "ls"}

// Names returns the complete tool palette.
func Names() []string { return append([]string(nil), names...) }

// Tools builds the agent-side tool set for one attempt. The palette selects a
// subset of Names; unknown names are ignored so a profile that mentions a tool
// this package lacks still runs with the rest.
// Every tool is confined to workspace: file paths are resolved inside it and
// commands run with it as their working directory.
func Tools(workspace string, palette []string, env []string) []fantasy.AgentTool {
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
			tools = append(tools, writeTool(workspace))
		case "edit":
			tools = append(tools, editTool(workspace))
		case "grep":
			tools = append(tools, grepTool(workspace, env))
		case "find":
			tools = append(tools, findTool(workspace, env))
		case "ls":
			tools = append(tools, lsTool(workspace, env))
		}
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
	Path string `json:"path" description:"Workspace-relative path of the file to read"`
}

type bashToolInput struct {
	Command string `json:"command" description:"Shell command to run with the workspace as its working directory"`
}

type writeToolInput struct {
	Path    string `json:"path" description:"Workspace-relative path of the file to write"`
	Content string `json:"content" description:"Complete new contents of the file"`
}

type editToolInput struct {
	Path    string `json:"path" description:"Workspace-relative path of the file to edit"`
	OldText string `json:"old_text" description:"Exact text to replace; must occur in the file"`
	NewText string `json:"new_text" description:"Replacement text"`
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

func readTool(workspace string) fantasy.AgentTool {
	return fantasy.NewAgentTool("read", "Read a file from the workspace. Returns at most 256 KiB.",
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
			data, err := io.ReadAll(io.LimitReader(file, maxToolReadBytes+1))
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			// File contents are returned verbatim: they are the model's own
			// working data (an edit must match them exactly), and this text
			// travels through the provider API, never through the terminal.
			if len(data) > maxToolReadBytes {
				cut := maxToolReadBytes
				for cut > 0 && !utf8.RuneStart(data[cut]) {
					cut--
				}
				return fantasy.NewTextResponse(string(data[:cut]) + "\n[file truncated at 256 KiB]"), nil
			}
			return fantasy.NewTextResponse(string(data)), nil
		})
}

func bashTool(workspace string, env []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("bash", "Run a shell command with the workspace as its working directory and return its combined output.",
		func(ctx context.Context, in bashToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// This is the adapter's only shell path:
			// the command is the model's own, and it runs in the workspace the
			// orchestrator prepared. It is never composed from task or trigger
			// text by the orchestrator itself.
			text, err := runWorkspaceCommand(ctx, workspace, env, "bash", "-c", in.Command)
			return commandResponse("bash", text, err), nil
		})
}

func writeTool(workspace string) fantasy.AgentTool {
	return fantasy.NewAgentTool("write", "Write a file in the workspace, replacing it if it exists.",
		func(_ context.Context, in writeToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
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
			return fantasy.NewTextResponse(fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path)), nil
		})
}

func editTool(workspace string) fantasy.AgentTool {
	return fantasy.NewAgentTool("edit", "Replace the first occurrence of exact text in a workspace file.",
		func(_ context.Context, in editToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			path, err := workspacePath(workspace, in.Path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if in.OldText == "" {
				return fantasy.NewTextErrorResponse("old_text is required"), nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if !strings.Contains(string(data), in.OldText) {
				return fantasy.NewTextErrorResponse("old_text not found in " + in.Path), nil
			}
			updated := strings.Replace(string(data), in.OldText, in.NewText, 1)
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("replaced 1 occurrence in %s (%d bytes removed, %d bytes added)",
				in.Path, len(in.OldText), len(in.NewText))), nil
		})
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
