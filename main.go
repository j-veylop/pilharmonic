package main

import (
	"errors"
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

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator:", err)
		os.Exit(1)
	}
}

func run() (err error) {
	state := flag.String("state", "", "path to the state file (default: user config directory)")
	workspace := flag.String("workspace", "", "working directory for new tasks (default: current directory)")
	specialistTools := flag.String("specialist-tools", "", "path to bundled Pi specialist tool limits (JSON)")
	agents := flag.String("agents", "", "path to optional agent command configuration JSON")
	profiles := flag.String("profiles", "", "profile and specialist settings (default: profiles.json in the current directory, if present)")
	profile := flag.String("profile", "", "default profile for routed and unchosen tasks (built-in profile uses executor defaults)")
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
		*workspace, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	if *state == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*state = filepath.Join(dir, "orchestrator", "state.json")
	}
	service, err := orchestrator.Open(*state)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, service.Shutdown()) }()

	if err := service.SetDefaultIsolation(*isolation); err != nil {
		return err
	}
	if *agents != "" {
		if err := service.LoadAgents(*agents); err != nil {
			return err
		}
	}
	service.LoadBuiltinSpecialists()
	if *specialistTools != "" {
		if err := service.LoadSpecialistTools(*specialistTools); err != nil {
			return err
		}
	}
	if err := loadStartupProfiles(service, *profiles); err != nil {
		return err
	}
	if *profile != "" {
		if err := service.SetRoutingProfile(*profile); err != nil {
			return err
		}
	}
	if !*subagents {
		*maxSubagentDepth, *maxSubagents = 0, 0
	}
	if err := service.SetSubagentLimits(*maxSubagentDepth, *maxSubagents); err != nil {
		return err
	}
	if *maxSubagentDepth > 0 && *maxSubagents > 0 {
		path := *subagentSocket
		if path == "" {
			path = filepath.Join(filepath.Dir(*state), "subagent.sock")
		}
		if err := service.EnableSubagentControl(path); err != nil {
			return err
		}
	}
	if err := service.SetConcurrencyLimit(*maxConcurrent); err != nil {
		return err
	}
	return tui.Run(service, *workspace)
}

func loadStartupProfiles(service *orchestrator.Service, path string) error {
	if path != "" {
		return service.LoadProfiles(path)
	}
	err := service.LoadProfiles("profiles.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
