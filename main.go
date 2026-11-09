package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"orchestrator/internal/orchestrator"
	"orchestrator/internal/tui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "subagent" {
		os.Exit(orchestrator.RunSubagentCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	state := flag.String("state", "", "path to the state file (default: user config directory)")
	workspace := flag.String("workspace", "", "working directory for new tasks (default: current directory)")
	agents := flag.String("agents", "", "path to optional agent command configuration JSON")
	maxConcurrent := flag.Int("max-concurrent", 2, "maximum simultaneous agent processes")
	isolation := flag.Bool("isolation", true, "start new tasks in an isolated Git worktree when possible")
	subagents := flag.Bool("subagents", true, "allow running agents to spawn child tasks")
	subagentSocket := flag.String("subagent-socket", "", "path to the local subagent control socket (default: beside the state file)")
	maxSubagentDepth := flag.Int("max-subagent-depth", orchestrator.DefaultMaxSubagentDepth, "maximum subagent delegation depth")
	maxSubagents := flag.Int("max-subagents", orchestrator.DefaultMaxSubagents, "maximum child tasks one task may spawn")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if *workspace == "" {
		var err error
		*workspace, err = os.Getwd()
		if err != nil {
			fail(err)
		}
	}
	if *state == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			fail(err)
		}
		*state = filepath.Join(dir, "orchestrator", "state.json")
	}
	service, err := orchestrator.Open(*state)
	if err != nil {
		fail(err)
	}
	if err := service.SetDefaultIsolation(*isolation); err != nil {
		fail(err)
	}
	if *agents != "" {
		if err := service.LoadAgents(*agents); err != nil {
			fail(err)
		}
	}
	if !*subagents {
		*maxSubagentDepth, *maxSubagents = 0, 0
	}
	if err := service.SetSubagentLimits(*maxSubagentDepth, *maxSubagents); err != nil {
		fail(err)
	}
	if *subagents && *maxSubagentDepth > 0 && *maxSubagents > 0 {
		path := *subagentSocket
		if path == "" {
			path = filepath.Join(filepath.Dir(*state), "subagent.sock")
		}
		if err := service.EnableSubagentControl(path); err != nil {
			fail(err)
		}
	}
	if err := service.SetConcurrencyLimit(*maxConcurrent); err != nil {
		fail(err)
	}
	runErr := tui.Run(service, *workspace)
	shutdownErr := service.Shutdown()
	if runErr != nil {
		fail(runErr)
	}
	if shutdownErr != nil {
		fail(shutdownErr)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, "orchestrator:", err); os.Exit(1) }
