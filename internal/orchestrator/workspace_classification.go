package orchestrator

import (
	"regexp"
	"strings"
)

// changeVerbs are imperative actions that normally request a writable,
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
	"help me ",
	"i need you to ",
	"i want you to ",
	"i'll ",
	"i'd like to ",
	"i would like to ",
	"kindly ",
	"let me ",
	"let's ",
	"please ",
	"we need to ",
	"we should ",
	"will you ",
	"would you ",
}

// requestLeadWords may precede a request verb without making it a noun phrase:
// subjects ("we should add tests"), modals ("we could add tests") and the
// auxiliary words of common contractions, which the tokenizer splits off.
var requestLeadWords = map[string]bool{
	"can": true, "could": true, "d": true, "had": true, "has": true,
	"have": true, "he": true, "help": true, "i": true, "it": true,
	"let": true, "like": true, "ll": true, "may": true, "me": true,
	"might": true, "must": true, "need": true, "s": true, "shall": true,
	"she": true, "should": true, "they": true, "want": true, "we": true,
	"will": true, "would": true, "you": true,
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

// taskMakesChanges reports whether the mandate explicitly requests a change.
// Only that signal creates an automatic worktree. The selected executor is not
// a mutation signal: read-only and ambiguous mandates share the source
// workspace. This is a lexical heuristic, not proof of what an agent will do,
// so a misleading mandate can still write to the shared workspace. Operators
// can explicitly enable isolation for any queued task. It checks the first
// request verb and verbs after punctuation or connectors, so nouns such as
// "fix" in "review the fix" are not requests.
func taskMakesChanges(text string) bool {
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
			// A subject or auxiliary may precede the verb. Keep the request-start
			// position so "we should refactor this" and "help me fix the parser"
			// still count as requested work.
			if requestLeadWords[token] && !changeVerbs[token] {
				continue
			}
		}
		atRequestStart = changeConnectors[token]
	}
	return false
}
