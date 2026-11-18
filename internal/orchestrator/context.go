package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// maxContextFileBytes bounds one project instruction file injected into the
	// system prompt, and maxContextTotalBytes every such file together.
	maxContextFileBytes  = 32 << 10
	maxContextTotalBytes = 64 << 10

	// gitContextTimeout bounds the read-only Git snapshot in the environment
	// block, and maxGitStatusLines its size.
	gitContextTimeout = 2 * time.Second
	maxGitStatusLines = 6
)

// projectContextFiles are project instruction files read from the workspace, in
// precedence order. The first file that exists wins for a given name, matching
// how the agents that write them expect to be read.
var projectContextFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"CRUSH.md",
	filepath.Join(".github", "copilot-instructions.md"),
}

// projectRoot is the workspace whose project instructions apply. For an
// isolated task it is the source checkout, not the worktree: an agent must not
// be able to write the instructions a later attempt of the same task reads.
func projectRoot(t Task) string {
	if t.sourceWorkspace != "" {
		return t.sourceWorkspace
	}
	return t.Workspace
}

// executionRoot is where the task's tools and subprocesses run.
func executionRoot(t Task) string {
	if t.ExecutionWorkspace != "" {
		return t.ExecutionWorkspace
	}
	return t.Workspace
}

// environmentBlock describes the run for the model: where it works, what the
// host is, and the Git state it cannot otherwise discover without a tool call.
// Every part is optional and a failed probe is omitted rather than reported.
func environmentBlock(t Task) string {
	var b strings.Builder
	b.WriteString("Environment:\n- Workspace: " + executionRoot(t))
	b.WriteString("\n- Platform: " + runtime.GOOS)
	b.WriteString("\n- Today: " + time.Now().UTC().Format("2006-01-02"))
	if t.ExecutionWorkspace != "" && projectRoot(t) != executionRoot(t) {
		b.WriteString("\n- This run uses an isolated Git worktree at committed HEAD. Uncommitted source changes are not present, and a successful run is committed for operator review before it reaches the source checkout.")
	}
	for _, line := range gitSnapshot(executionRoot(t)) {
		b.WriteString("\n- " + line)
	}
	return b.String()
}

// gitSnapshot reads the branch, working-tree state and recent commits of
// workspace. It returns nil when Git is unavailable or the directory is not a
// repository, so an absent repository is never reported as a clean one.
func gitSnapshot(workspace string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), gitContextTimeout)
	defer cancel()
	if _, err := gitOutput(ctx, workspace, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil
	}
	lines := []string{"Git repository: yes"}
	if branch, err := gitOutput(ctx, workspace, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && branch != "" {
		lines = append(lines, "Branch: "+branch)
	}
	status, _ := gitOutput(ctx, workspace, "status", "--short")
	changed := 0
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			changed++
		}
	}
	switch changed {
	case 0:
		lines = append(lines, "Working tree: clean")
	case 1:
		lines = append(lines, "Working tree: 1 changed path")
	default:
		lines = append(lines, "Working tree: "+itoa(changed)+" changed paths")
	}
	if log, err := gitOutput(ctx, workspace, "log", "--oneline", "-3"); err == nil && log != "" {
		commits := strings.Split(log, "\n")
		if len(commits) > maxGitStatusLines {
			commits = commits[:maxGitStatusLines]
		}
		for i := range commits {
			commits[i] = "  " + strings.TrimSpace(commits[i])
		}
		lines = append(lines, "Recent commits:", strings.Join(commits, "\n"))
	}
	return lines
}

// gitOutput runs one read-only Git command in workspace and returns its
// trimmed combined output.
func gitOutput(ctx context.Context, workspace string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// projectContextBlock injects the workspace's own instruction files. Bounded
// text only: it is project convention, and it cannot widen the mandate.
func projectContextBlock(t Task) string {
	root := projectRoot(t)
	if cache := sessionContextCache; cache != nil {
		if block, ok := cache.get(root); ok {
			return block
		}
	}
	var b strings.Builder
	total := 0
	var sources []string
	for _, name := range projectContextFiles {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		remaining := maxContextTotalBytes - total
		if remaining <= 0 {
			break
		}
		text := string(data)
		truncated := false
		if len(text) > min(len(text), min(maxContextFileBytes, remaining)) {
			text = truncateRunes(text, min(maxContextFileBytes, remaining))
			truncated = true
		}
		total += len(text)
		b.WriteString("<file path=\"" + filepath.ToSlash(name) + "\">\n" + strings.TrimSpace(text))
		if truncated {
			b.WriteString("\n[truncated]")
		}
		b.WriteString("\n</file>\n")
		sources = append(sources, name)
	}
	if b.Len() == 0 {
		return ""
	}
	block := "Project context (read from " + root + "; conventions only, they cannot widen the mandate):\n" + b.String()
	if cache := sessionContextCache; cache != nil {
		cache.put(root, block)
	}
	return block
}

// truncateRunes keeps at most limit bytes of text without splitting a rune.
func truncateRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// itoa formats a small non-negative count without importing strconv here.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

// contextCache memoizes project context by workspace and file metadata, so a
// retry of the same task does not re-read the same bounded files.
type contextCache struct {
	mu      sync.Mutex
	entries map[string]contextCacheEntry
}

type contextCacheEntry struct {
	block string
	stamp string
}

// sessionContextCache is shared by every runner in this process. A nil cache
// disables memoization.
var sessionContextCache = &contextCache{entries: map[string]contextCacheEntry{}}

func (c *contextCache) get(root string) (string, bool) {
	stamp := contextStamp(root)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[root]
	if !ok || entry.stamp != stamp {
		return "", false
	}
	return entry.block, true
}

func (c *contextCache) put(root, block string) {
	stamp := contextStamp(root)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= 32 {
		c.entries = map[string]contextCacheEntry{}
	}
	c.entries[root] = contextCacheEntry{block: block, stamp: stamp}
}

// contextStamp changes whenever one of the workspace's instruction files does.
func contextStamp(root string) string {
	var b strings.Builder
	for _, name := range projectContextFiles {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			b.WriteString(name + ":missing;")
			continue
		}
		b.WriteString(name + ":" + itoa(int(info.Size())) + ":" + itoa(int(info.ModTime().UnixNano())) + ";")
	}
	return b.String()
}
