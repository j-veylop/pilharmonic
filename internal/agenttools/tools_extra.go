package agenttools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
)

// Bounded discovery keeps one tool call from flooding the model's context.
const (
	maxGlobResults  = 200
	maxSearchHits   = 200
	maxSearchLine   = 500
	maxSearchFile   = 2 << 20
	maxDiagnosticGo = 20
)

// skipDir reports whether a directory never carries useful model context.
func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", ".cache":
		return true
	}
	return false
}

// matchGlob matches a slash-separated path against a pattern that supports
// `**` for any number of segments and the usual single-segment wildcards.
func matchGlob(pattern, name string) bool {
	pattern = filepath.ToSlash(strings.TrimPrefix(pattern, "./"))
	name = filepath.ToSlash(strings.TrimPrefix(name, "./"))
	if !strings.Contains(pattern, "/") {
		matched, _ := filepath.Match(pattern, filepath.Base(name))
		return matched
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, _ := filepath.Match(pattern[0], name[0])
	return matched && matchSegments(pattern[1:], name[1:])
}

func globTool(workspace string) fantasy.AgentTool {
	type input struct {
		Pattern string `json:"pattern" description:"Glob pattern relative to the workspace, for example **/*.go or docs/*.md"`
	}
	return fantasy.NewAgentTool("glob", "List workspace files whose relative path matches a glob pattern. Git metadata and dependency directories are skipped.",
		func(ctx context.Context, in input, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			pattern := strings.TrimSpace(in.Pattern)
			if pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}
			var matches []string
			err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if entry.IsDir() {
					if path != workspace && skipDir(entry.Name()) {
						return filepath.SkipDir
					}
					return nil
				}
				rel, err := filepath.Rel(workspace, path)
				if err != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)
				if matchGlob(pattern, rel) {
					matches = append(matches, rel)
					if len(matches) >= maxGlobResults {
						return errLimitReached
					}
				}
				return nil
			})
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fantasy.NewTextErrorResponse(ctxErr.Error()), nil
			}
			if err != nil && !errors.Is(err, errLimitReached) {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			sort.Strings(matches)
			if len(matches) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("glob: no matches for %q", Sanitize(pattern))), nil
			}
			text := strings.Join(matches, "\n")
			if errors.Is(err, errLimitReached) {
				text += fmt.Sprintf("\n[stopped after %d matches]", maxGlobResults)
			}
			return fantasy.NewTextResponse(text), nil
		})
}

var errLimitReached = errors.New("result limit reached")

func rgTool(workspace string) fantasy.AgentTool {
	type input struct {
		Pattern    string `json:"pattern" description:"Go regular expression to search for"`
		Path       string `json:"path,omitempty" description:"Optional workspace-relative file or directory; defaults to the workspace root"`
		Glob       string `json:"glob,omitempty" description:"Optional glob limiting which files are searched, for example *.go"`
		IgnoreCase bool   `json:"ignore_case,omitempty" description:"Match case-insensitively"`
	}
	return fantasy.NewAgentTool("rg", "Search file contents with a regular expression and return file:line:text matches. Git metadata and dependency directories are skipped.",
		func(ctx context.Context, in input, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			pattern := strings.TrimSpace(in.Pattern)
			if pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}
			if in.IgnoreCase {
				pattern = "(?i)" + pattern
			}
			expr, err := regexp.Compile(pattern)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			root := workspace
			if path := strings.TrimSpace(in.Path); path != "" {
				if root, err = workspacePath(workspace, path); err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
			}
			glob := strings.TrimSpace(in.Glob)
			var hits []string
			search := func(path string, info os.DirEntry) error {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if info.IsDir() {
					if path != root && skipDir(info.Name()) {
						return filepath.SkipDir
					}
					return nil
				}
				rel, relErr := filepath.Rel(workspace, path)
				if relErr != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)
				if glob != "" && !matchGlob(glob, rel) {
					return nil
				}
				fileInfo, statErr := info.Info()
				if statErr != nil || fileInfo.Size() > maxSearchFile {
					return nil
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil || strings.ContainsRune(string(data), 0) {
					return nil
				}
				for number, line := range strings.Split(string(data), "\n") {
					if !expr.MatchString(line) {
						continue
					}
					if len(line) > maxSearchLine {
						line = line[:maxSearchLine] + "..."
					}
					hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, number+1, line))
					if len(hits) >= maxSearchHits {
						return errLimitReached
					}
				}
				return nil
			}
			var walkErr error
			if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
				walkErr = search(root, fileStatEntry{info})
			} else {
				walkErr = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					return search(path, entry)
				})
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fantasy.NewTextErrorResponse(ctxErr.Error()), nil
			}
			if walkErr != nil && !errors.Is(walkErr, errLimitReached) {
				return fantasy.NewTextErrorResponse(walkErr.Error()), nil
			}
			if len(hits) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("rg: no matches for %q", Sanitize(in.Pattern))), nil
			}
			text := strings.Join(hits, "\n")
			if errors.Is(walkErr, errLimitReached) {
				text += fmt.Sprintf("\n[stopped after %d matches]", maxSearchHits)
			}
			return fantasy.NewTextResponse(text), nil
		})
}

// fileStatEntry adapts os.FileInfo to os.DirEntry for the single-file search
// path, so directory walking and a direct file operand share one scanner.
type fileStatEntry struct{ os.FileInfo }

func (e fileStatEntry) Type() os.FileMode          { return e.FileInfo.Mode().Type() }
func (e fileStatEntry) Info() (os.FileInfo, error) { return e.FileInfo, nil }

type multiEditOperation struct {
	Path    string `json:"path" description:"Workspace-relative path of the file to edit"`
	OldText string `json:"old_text" description:"Text to replace; exact text is preferred, otherwise a whitespace-tolerant match is used"`
	NewText string `json:"new_text" description:"Replacement text"`
}

func multiEditTool(workspace string) fantasy.AgentTool {
	type input struct {
		Edits []multiEditOperation `json:"edits" description:"Ordered edits to apply"`
	}
	return fantasy.NewAgentTool("multiedit", "Apply several find-and-replace edits in order. Each edit uses the edit tool's exact match, with a whitespace-tolerant fallback.",
		func(_ context.Context, in input, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if len(in.Edits) == 0 {
				return fantasy.NewTextErrorResponse("edits is required"), nil
			}
			changed := make(map[string]int, len(in.Edits))
			for i, edit := range in.Edits {
				if _, err := applyEdit(workspace, edit.Path, edit.OldText, edit.NewText); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("edit %d of %d (%s): %v", i+1, len(in.Edits), edit.Path, err)), nil
				}
				changed[edit.Path]++
			}
			var files []string
			for path := range changed {
				files = append(files, path)
			}
			sort.Strings(files)
			return fantasy.NewTextResponse(fmt.Sprintf("applied %d edits across %d file(s): %s", len(in.Edits), len(files), strings.Join(files, ", "))), nil
		})
}

func diagnosticsTool(workspace string, run func(context.Context, string, []string) string) fantasy.AgentTool {
	type input struct {
		Paths []string `json:"paths,omitempty" description:"Optional workspace-relative Go files or package directories; defaults to every Go file under the workspace"`
	}
	return fantasy.NewAgentTool("diagnostics", "Report compiler and static-check diagnostics for Go files using gopls when available, otherwise go vet.",
		func(ctx context.Context, in input, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if run != nil {
				text := strings.TrimSpace(run(ctx, workspace, in.Paths))
				if text == "" {
					return fantasy.NewTextResponse("diagnostics: no issues found"), nil
				}
				return fantasy.NewTextResponse(text), nil
			}
			if _, err := os.Stat(filepath.Join(workspace, "go.mod")); err != nil {
				return fantasy.NewTextResponse("diagnostics: no Go module in workspace"), nil
			}
			targets, err := diagnosticTargets(workspace, in.Paths)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if len(targets) == 0 {
				return fantasy.NewTextResponse("diagnostics: no Go files found"), nil
			}
			ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			var text string
			if _, lookErr := exec.LookPath("gopls"); lookErr == nil {
				text, err = runWorkspaceCommand(ctx, workspace, nil, "gopls", append([]string{"check"}, targets...)...)
			} else if _, lookErr := exec.LookPath("go"); lookErr == nil {
				dirs := packageDirs(targets)
				text, err = runWorkspaceCommand(ctx, workspace, nil, "go", append([]string{"vet"}, dirs...)...)
			} else {
				return fantasy.NewTextResponse("diagnostics: neither gopls nor go is installed"), nil
			}
			text = strings.TrimSpace(text)
			if err != nil {
				if text == "" {
					return fantasy.NewTextErrorResponse("diagnostics failed: " + err.Error()), nil
				}
				return fantasy.NewTextErrorResponse(text), nil
			}
			if text == "" {
				return fantasy.NewTextResponse("diagnostics: no issues found"), nil
			}
			return fantasy.NewTextResponse(text), nil
		})
}

func diagnosticTargets(workspace string, paths []string) ([]string, error) {
	if len(paths) > 0 {
		targets := make([]string, 0, len(paths))
		for _, path := range paths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if _, err := workspacePath(workspace, path); err != nil {
				return nil, err
			}
			targets = append(targets, filepath.ToSlash(path))
		}
		return targets, nil
	}
	var targets []string
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != workspace && skipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			return nil
		}
		targets = append(targets, filepath.ToSlash(rel))
		if len(targets) >= maxDiagnosticGo {
			return errLimitReached
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return nil, err
	}
	return targets, nil
}

// packageDirs names the packages containing changed Go files, relative to the
// workspace, so a fallback vet checks each package once.
func packageDirs(files []string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, file := range files {
		if !strings.HasSuffix(file, ".go") {
			dirs = appendUnique(dirs, seen, file)
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(file))
		if dir == "." {
			dir = "./."
		} else if !strings.HasPrefix(dir, ".") {
			dir = "./" + dir
		}
		dirs = appendUnique(dirs, seen, dir)
	}
	return dirs
}

func appendUnique(items []string, seen map[string]bool, value string) []string {
	if seen[value] {
		return items
	}
	seen[value] = true
	return append(items, value)
}

// replaceWithWhitespaceTolerance finds the first block of lines whose trimmed
// text equals the trimmed old text, then re-indents newText to the block's
// actual indentation. It handles a model echoing the right lines with slightly
// different leading or trailing whitespace.
func replaceWithWhitespaceTolerance(content, oldText, newText string) (string, bool) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(strings.TrimRight(oldText, "\n"), "\n")
	if len(oldLines) == 0 {
		return content, false
	}
	indent := func(line string) string { return line[:len(line)-len(strings.TrimLeft(line, " \t"))] }
	for start := 0; start+len(oldLines) <= len(contentLines); start++ {
		matched := true
		for offset, oldLine := range oldLines {
			if strings.TrimSpace(contentLines[start+offset]) != strings.TrimSpace(oldLine) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		oldIndent := indent(oldLines[0])
		actualIndent := indent(contentLines[start])
		oldUnit := indentUnit(oldLines, oldIndent, "    ")
		actualUnit := indentUnit(contentLines[start:start+len(oldLines)], actualIndent, "\t")
		newLines := strings.Split(strings.TrimRight(newText, "\n"), "\n")
		for i, line := range newLines {
			if strings.TrimSpace(line) == "" {
				newLines[i] = ""
				continue
			}
			lead := indent(line)
			if strings.HasPrefix(lead, oldIndent) {
				levels := ((len(lead) - len(oldIndent)) + len(oldUnit) - 1) / len(oldUnit)
				newLines[i] = actualIndent + strings.Repeat(actualUnit, levels) + line[len(lead):]
			}
		}
		replacement := append([]string{}, contentLines[:start]...)
		replacement = append(replacement, newLines...)
		replacement = append(replacement, contentLines[start+len(oldLines):]...)
		return strings.Join(replacement, "\n"), true
	}
	return content, false
}

// indentUnit reports the additional whitespace one nesting level adds. It
// looks at the block lines first and falls back to the supplied default.
func indentUnit(lines []string, base, fallback string) string {
	for _, line := range lines {
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if len(lead) > len(base) && strings.HasPrefix(lead, base) {
			return lead[len(base):]
		}
	}
	return fallback
}
