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
	state := flag.String("state", "", "path to the state file (default: user config directory)")
	workspace := flag.String("workspace", "", "working directory for new tasks (default: current directory)")
	agents := flag.String("agents", "", "path to optional agent command configuration JSON")
	maxConcurrent := flag.Int("max-concurrent", 2, "maximum simultaneous agent processes")
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
	if *agents != "" {
		if err := service.LoadAgents(*agents); err != nil {
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
