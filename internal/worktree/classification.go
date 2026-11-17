package worktree

import (
	"regexp"
	"strings"
)

// changeVerbs are imperative actions that normally request a writable,
// isolated workspace. Only verbs in request position count, so inspecting an
// existing change still stays in the source workspace.
var changeVerbs = wordSet(`
	add adjust alter apply build bump change checkout cherry chmod chown
	clean cleanup combine commit consolidate convert correct create delete
	deprecate disable document drop edit enable enhance expand expose
	extend extract fix format generate gofmt implement improve increase
	inline insert install integrate introduce make merge migrate modify
	move normalise normalize mv patch optimise optimize port pull push
	prune purge rebase rebuild redesign reduce refactor remove rename
	replace reset restore restructure revert revise rework rewrite rm
	rmdir set simplify split stage standardise standardize stash
	strengthen support switch touch truncate update upgrade vendor write`)

// gitWorktreeMutations are Git worktree subcommands that modify the source
// repository. They need isolation even though the surrounding request opens
// with "git". Read-only forms such as "git worktree list" stay shared.
var gitWorktreeMutations = wordSet("add lock move prune remove repair unlock")

// changeConnectors start a new request phrase. An edit verb after one of these
// is still requested work. The commands "run" and "git" are treated as
// connectors only at a request start so "run gofmt" and "git commit" count,
// while "how to run gofmt" remains an inspection question.
var changeConnectors = wordSet("also and now please then")

var mandateTokenPattern = regexp.MustCompile(`[a-z0-9_]+|[.;:,!?]`)

var requestPoliteness = []string{
	"can you ", "could you ", "help me ", "i need you to ", "i want you to ", "i'll ", "i'd like to ",
	"i would like to ", "kindly ", "let me ", "let's ", "please ", "we need to ", "we should ",
	"will you ", "would you ",
}

// requestLeadWords may precede a request verb without making it a noun phrase:
// subjects ("we should add tests"), modals ("we could add tests") and the
// auxiliary words of common contractions, which the tokenizer splits off.
var requestLeadWords = wordSet(`
	can could d had has have he help i it let like ll may me might must
	need s shall she should they want we will would you`)

func wordSet(words string) map[string]bool {
	set := make(map[string]bool)
	for _, word := range strings.Fields(words) {
		set[word] = true
	}
	return set
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

// RequestsChanges reports whether the mandate explicitly requests a change.
// Only that signal creates an automatic worktree. The selected executor is not
// a mutation signal: read-only and ambiguous mandates share the source
// workspace. This is a lexical heuristic, not proof of what an agent will do,
// so a misleading mandate can still write to the shared workspace. Operators
// can explicitly enable isolation for any queued task. It checks the first
// request verb and verbs after punctuation or connectors, so nouns such as
// "fix" in "review the fix" are not requests.
func RequestsChanges(text string) bool {
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
			if requestLeadWords[token] {
				continue
			}
		}
		atRequestStart = changeConnectors[token]
	}
	return false
}
