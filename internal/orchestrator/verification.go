package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxVerifyRounds bounds automatic verification retries so a model cannot be
// trapped in an edit/verify loop.
const maxVerifyRounds = 3

// verificationTimeout bounds one automatic check run.
const verificationTimeout = 90 * time.Second

// verifiedTool reports whether a tool changes workspace files.
func verifiedTool(name string) bool {
	switch name {
	case "edit", "write", "multiedit":
		return true
	}
	return false
}

// editedPaths extracts the workspace paths one file-changing tool call touched.
func editedPaths(name, input string) []string {
	var payload struct {
		Path  string `json:"path"`
		Edits []struct {
			Path string `json:"path"`
		} `json:"edits"`
	}
	if json.Unmarshal([]byte(input), &payload) != nil {
		return nil
	}
	switch name {
	case "multiedit":
		var paths []string
		for _, edit := range payload.Edits {
			if edit.Path != "" {
				paths = append(paths, edit.Path)
			}
		}
		return paths
	case "edit", "write":
		if payload.Path != "" {
			return []string{payload.Path}
		}
	}
	return nil
}

// verificationReport runs the detected stacks' format, diagnostic and test
// checks after edits and returns a model-readable failure summary plus a notice
// naming any step it could not run. Both are "" when the checks pass and every
// tool was available.
func verificationReport(ctx context.Context, workspace string, files []string, checks *checkSettings) (string, string) {
	specs := detectedChecks(workspace, files, checks)
	if len(specs) == 0 {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, verificationTimeout)
	defer cancel()
	var problems []string
	for _, spec := range specs {
		owned := filesForSpec(spec, files)
		if out := runFormat(ctx, workspace, spec, owned); out != "" {
			problems = append(problems, spec.Name+" format:\n"+out)
		}
		if out := runLint(ctx, workspace, spec, owned); out != "" {
			problems = append(problems, spec.Name+" diagnostics:\n"+out)
		}
	}
	if len(problems) == 0 {
		for _, spec := range specs {
			if out := runTests(ctx, workspace, spec, filesForSpec(spec, files)); out != "" {
				problems = append(problems, spec.Name+" tests:\n"+out)
			}
		}
	}
	skipped := ""
	if names := skippedChecks(specs); len(names) > 0 {
		skipped = "Checks skipped because the tool is not installed: " + strings.Join(names, ", ") + ". Their absence is not a pass."
	}
	if len(problems) == 0 {
		return "", skipped
	}
	report := "Automatic verification found problems after your edits (not new authority; fix them or report why they are unrelated):\n" + strings.Join(problems, "\n\n")
	return trimToRuneStart(sanitizeCheckText(report), 16<<10), skipped
}

// skippedChecks names the tools an enabled stack needs but this host does not
// have. A lint step covered by an installed fallback is not reported.
func skippedChecks(specs []checkSpec) []string {
	seen := map[string]bool{}
	var missing []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		missing = append(missing, name)
	}
	for _, spec := range specs {
		for _, argv := range [][]string{spec.Format, spec.Test} {
			if len(argv) > 0 && !commandExists(argv[0]) {
				add(argv[0])
			}
		}
		if len(spec.Lint) > 0 && !commandExists(spec.Lint[0]) {
			if len(spec.Fallback) == 0 || !commandExists(spec.Fallback[0]) {
				add(spec.Lint[0])
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// commandExists reports whether name resolves on this host.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// sanitizeCheckText removes terminal control bytes from command output before
// it enters the conversation.
func sanitizeCheckText(text string) string {
	var clean strings.Builder
	clean.Grow(len(text))
	for _, r := range text {
		if r == '\n' || r == '\t' || r >= 0x20 && (r < 0x7f || r > 0x9f) {
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

func runFormat(ctx context.Context, workspace string, spec checkSpec, files []string) string {
	output, _ := runCheck(ctx, workspace, spec.Format, files)
	return output
}

func runLint(ctx context.Context, workspace string, spec checkSpec, files []string) string {
	output, err := runCheck(ctx, workspace, spec.Lint, files)
	if output == "" && err != nil && len(spec.Fallback) > 0 {
		output, _ = runCheck(ctx, workspace, spec.Fallback, files)
	}
	return output
}

func runTests(ctx context.Context, workspace string, spec checkSpec, files []string) string {
	if spec.ID == "go" {
		var packages []string
		for _, pkg := range packagesFor(files) {
			if packageHasTests(workspace, pkg) {
				packages = append(packages, pkg)
			}
		}
		if len(packages) == 0 {
			return ""
		}
		output, err := runArgv(ctx, workspace, append([]string{"go", "test"}, packages...))
		if err != nil {
			return output
		}
		return ""
	}
	output, err := runCheck(ctx, workspace, spec.Test, files)
	if err != nil {
		return output
	}
	return ""
}

// runCheck expands one argv template and runs it. {files} becomes the changed
// files of the stack and {packages} their package directories. Missing
// binaries are skipped silently so an absent tool never looks like a pass or a
// failure.
func runCheck(ctx context.Context, workspace string, argv, files []string) (string, error) {
	if len(argv) == 0 {
		return "", nil
	}
	args := make([]string, 0, len(argv))
	for _, token := range argv {
		switch token {
		case "{files}":
			args = append(args, files...)
		case "{packages}":
			args = append(args, packagesFor(files)...)
		default:
			args = append(args, token)
		}
	}
	return runArgv(ctx, workspace, args)
}

func runArgv(ctx context.Context, workspace string, argv []string) (string, error) {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func packagesFor(files []string) []string {
	seen := make(map[string]bool)
	var packages []string
	for _, file := range files {
		dir := filepath.ToSlash(filepath.Dir(file))
		if dir == "." || dir == "" {
			dir = "."
		} else if !strings.HasPrefix(dir, ".") {
			dir = "./" + dir
		}
		if seen[dir] {
			continue
		}
		seen[dir] = true
		packages = append(packages, dir)
	}
	sort.Strings(packages)
	return packages
}

func packageHasTests(workspace, pkg string) bool {
	dir := filepath.Join(workspace, strings.TrimPrefix(pkg, "./"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "_test.go") {
			return true
		}
	}
	return false
}
