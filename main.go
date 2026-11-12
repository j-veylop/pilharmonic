package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/tui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "subagent" {
		os.Exit(orchestrator.RunSubagentCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	if err := run(); err != nil {
		code := 1
		if errors.Is(err, errUsage) {
			code = 2
		}
		fmt.Fprintln(os.Stderr, "pilharmonic:", err)
		os.Exit(code)
	}
}

// version and commit are set at build time, for example
// -ldflags "-X main.version=$(git describe --tags --always) -X main.commit=$(git rev-parse HEAD)".
var (
	version = "devel"
	commit  = ""
)

// errUsage marks a command-line usage error, mapped to exit code 2 in main.
var errUsage = errors.New("usage")

func versionString() string {
	if commit == "" {
		return version
	}
	return version + " (" + commit + ")"
}

func run() (err error) {
	state := flag.String("state", "", "path to the state file (default: user config directory)")
	workspace := flag.String("workspace", "", "working directory for new tasks (default: current directory)")
	specialistTools := flag.String("specialist-tools", "", "path to bundled specialist tool limits (JSON)")
	model := flag.String("model", "", "default model for agents (provider/model); a profile's model overrides it")
	agents := flag.String("agents", "", "path to optional agent command configuration JSON")
	profiles := flag.String("profiles", "", "profile and specialist settings (default: profiles.json beside the state file)")
	profile := flag.String("profile", "", "default profile for routed and unchosen tasks (built-in profile uses executor defaults)")
	maxConcurrent := flag.Int("max-concurrent", orchestrator.DefaultConcurrency, "maximum simultaneous agent processes")
	idleTimeout := flag.Duration("idle-timeout", 0, "cancel an agent that produces no output for this long (0 disables)")
	isolation := flag.Bool("isolation", true, "allow automatic Git worktrees for mandates that request changes")
	subagents := flag.Bool("subagents", true, "allow running agents to spawn child tasks")
	subagentSocket := flag.String("subagent-socket", "", "path to the local subagent control socket (default: beside the state file)")
	maxSubagentDepth := flag.Int("max-subagent-depth", orchestrator.DefaultMaxSubagentDepth, "maximum subagent delegation depth")
	maxSubagents := flag.Int("max-subagents", orchestrator.DefaultMaxSubagents, "maximum child tasks one task may spawn")
	attemptRetention := flag.Int("attempt-retention", orchestrator.DefaultAttemptRetention, "attempt outputs kept in full per task (0 keeps every attempt)")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("%w: unexpected arguments: %s", errUsage, strings.Join(flag.Args(), " "))
	}
	if *showVersion {
		fmt.Println("pilharmonic " + versionString())
		return nil
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
	release, err := orchestrator.AcquireInstanceLock(*state)
	if err != nil {
		return err
	}
	defer release()
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
	profilesPath := *profiles
	profilesRequired := *profiles != ""
	if profilesPath == "" {
		profilesPath = filepath.Join(filepath.Dir(*state), "profiles.json")
	}
	if err := loadStartupProfiles(service, profilesPath, profilesRequired); err != nil {
		return err
	}
	if _, err := os.Stat(profilesPath); err == nil {
		fmt.Fprintf(os.Stderr, "pilharmonic: loaded profiles from %s\n", profilesPath)
	}
	if *model != "" {
		service.SetDefaultModel(*model)
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
	if err := service.SetIdleTimeout(*idleTimeout); err != nil {
		return err
	}
	if err := service.SetAttemptRetention(*attemptRetention); err != nil {
		return err
	}
	return tui.Run(service, *workspace)
}

// loadStartupProfiles loads an optional profiles file. A missing file is
// ignored unless required is set (the path was given explicitly on the
// command line), so an absent default never fails startup.
func loadStartupProfiles(service *orchestrator.Service, path string, required bool) error {
	if path == "" {
		return nil
	}
	err := service.LoadProfiles(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	return err
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "Usage:\n  pilharmonic [flags]\n  pilharmonic subagent [flags]\n\nFlags:\n")
	flag.PrintDefaults()
}
