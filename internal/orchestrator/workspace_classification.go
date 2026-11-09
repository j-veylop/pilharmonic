package orchestrator

import (
	"regexp"
	"strings"
)

// Inspection roles are expected to inspect the repository without changing it.
// Their bundled prompts leave the workspace unchanged unless the mandate asks
// for edits. The check below still isolates an inspection role when the mandate
// opens an edit action.
var inspectionRoles = map[string]bool{
	"debugger": true,
	"explorer": true,
	"gitter":   true,
	"reviewer": true,
	"tester":   true,
}

// changeVerbs are imperative actions that normally require a writable,
// isolated workspace. Only verbs in request position count, so inspecting an
// existing change still stays in the source workspace.
var changeVerbs = map[string]bool{
	"add":         true,
	"adjust":      true,
	"alter":       true,
	"apply":       true,
	"build":       true,
	"bump":        true,
	"change":      true,
	"checkout":    true,
	"cherry":      true,
	"chmod":       true,
	"chown":       true,
	"clean":       true,
	"cleanup":     true,
	"combine":     true,
	"commit":      true,
	"consolidate": true,
	"convert":     true,
	"correct":     true,
	"create":      true,
	"delete":      true,
	"deprecate":   true,
	"disable":     true,
	"document":    true,
	"drop":        true,
	"edit":        true,
	"enable":      true,
	"enhance":     true,
	"expand":      true,
	"expose":      true,
	"extend":      true,
	"extract":     true,
	"fix":         true,
	"format":      true,
	"generate":    true,
	"gofmt":       true,
	"implement":   true,
	"improve":     true,
	"increase":    true,
	"inline":      true,
	"insert":      true,
	"install":     true,
	"integrate":   true,
	"introduce":   true,
	"make":        true,
	"merge":       true,
	"migrate":     true,
	"modify":      true,
	"move":        true,
	"normalise":   true,
	"normalize":   true,
	"mv":          true,
	"patch":       true,
	"optimise":    true,
	"optimize":    true,
	"port":        true,
	"pull":        true,
	"push":        true,
	"prune":       true,
	"purge":       true,
	"rebase":      true,
	"rebuild":     true,
	"redesign":    true,
	"reduce":      true,
	"refactor":    true,
	"remove":      true,
	"rename":      true,
	"replace":     true,
	"reset":       true,
	"restore":     true,
	"restructure": true,
	"revert":      true,
	"revise":      true,
	"rework":      true,
	"rewrite":     true,
	"rm":          true,
	"rmdir":       true,
	"set":         true,
	"simplify":    true,
	"split":       true,
	"stage":       true,
	"standardise": true,
	"standardize": true,
	"stash":       true,
	"strengthen":  true,
	"support":     true,
	"switch":      true,
	"touch":       true,
	"truncate":    true,
	"update":      true,
	"upgrade":     true,
	"vendor":      true,
	"write":       true,
}

// gitWorktreeMutations are Git worktree subcommands that modify the source
// repository. They need isolation even though the surrounding request opens
// with "git". Read-only forms such as "git worktree list" stay shared.
var gitWorktreeMutations = map[string]bool{
	"add":    true,
	"lock":   true,
	"move":   true,
	"prune":  true,
	"remove": true,
	"repair": true,
	"unlock": true,
}

// changeConnectors start a new request phrase. An edit verb after one of these
// is still requested work. The commands "run" and "git" are treated as
// connectors only at a request start so "run gofmt" and "git commit" count,
// while "how to run gofmt" remains an inspection question.
var changeConnectors = map[string]bool{
	"also":   true,
	"and":    true,
	"now":    true,
	"please": true,
	"then":   true,
}

var mandateTokenPattern = regexp.MustCompile(`[a-z0-9_]+|[.;:,!?]`)

var requestPoliteness = []string{
	"can you ",
	"could you ",
	"i need you to ",
	"i want you to ",
	"kindly ",
	"please ",
	"will you ",
	"would you ",
}

var inspectionOpenings = []string{
	"analyse",
	"analyze",
	"assess",
	"audit",
	"check",
	"compare",
	"debug",
	"describe",
	"determine",
	"diagnose",
	"evaluate",
	"explain",
	"explore",
	"find",
	"git",
	"grep",
	"how",
	"identify",
	"inspect",
	"investigate",
	"list",
	"locate",
	"look",
	"read",
	"reproduce",
	"review",
	"run a test",
	"run test",
	"run the test",
	"run the tests",
	"search",
	"see",
	"show",
	"summarize",
	"test",
	"trace",
	"understand",
	"validate",
	"verify",
	"view",
	"what",
	"when",
	"where",
	"which",
	"who",
	"why",
}

func trimRequestPoliteness(text string) string {
	text = strings.TrimSpace(text)
	for {
		changed := false
		for _, prefix := range requestPoliteness {
			if strings.HasPrefix(text, prefix) {
				text = strings.TrimSpace(text[len(prefix):])
				changed = true
			}
		}
		if !changed {
			return text
		}
	}
}

// taskMakesChanges is the workspace classification used when default isolation
// is enabled. It is deliberately conservative: inspection requests and known
// inspection roles share the source workspace only when no edit action is
// requested. Requests matching neither signal retain automatic isolation.
//
// This is a lexical role heuristic, not proof of what an agent will do. A
// misleading mandate or a custom executor using an inspection role name can
// still write to the shared workspace. Operators can explicitly enable
// isolation for any queued task.
func taskMakesChanges(prompt, agent string) bool {
	if mandateRequestsChanges(prompt) {
		return true
	}
	if inspectionRequest(prompt) {
		return false
	}
	return !inspectionRoles[agent]
}

// inspectionRequest recognizes an inspection opening even when the selected
// executor is the general-purpose fallback. The action rules use the same
// kind of prefix matching as executor selection.
func inspectionRequest(prompt string) bool {
	opening := requestOpening(prompt)
	for _, prefix := range inspectionOpenings {
		if opening == prefix || strings.HasPrefix(opening, prefix+" ") {
			return true
		}
	}
	return false
}

func requestOpening(prompt string) string {
	text := trimRequestPoliteness(strings.ToLower(prompt))
	tokens := mandateTokenPattern.FindAllString(text, -1)
	words := make([]string, 0, 4)
	for _, token := range tokens {
		switch token {
		case ".", ";", ":", ",", "!", "?":
			return strings.Join(words, " ")
		}
		words = append(words, token)
	}
	return strings.Join(words, " ")
}

// mandateRequestsChanges reports whether the mandate contains an imperative
// edit action. It checks the first request verb and verbs after punctuation or
// connectors. Nouns such as "fix" in "review the fix" are not requests.
func mandateRequestsChanges(text string) bool {
	text = trimRequestPoliteness(strings.ToLower(text))
	text = strings.ReplaceAll(text, "\n", " ; ")
	tokens := mandateTokenPattern.FindAllString(text, -1)
	atRequestStart := true
	for i, token := range tokens {
		switch token {
		case ".", ";", ":", ",", "!", "?":
			atRequestStart = true
			continue
		}
		if atRequestStart {
			if changeVerbs[token] {
				return true
			}
			// "check out" is a Git mutation even though "check" alone is
			// normally an inspection request.
			if token == "check" && i+1 < len(tokens) && tokens[i+1] == "out" {
				return true
			}
			if token == "git" {
				if i+2 < len(tokens) && tokens[i+1] == "worktree" && gitWorktreeMutations[tokens[i+2]] {
					return true
				}
				continue
			}
			if token == "run" {
				continue
			}
		}
		atRequestStart = changeConnectors[token]
	}
	return false
}
