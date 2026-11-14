package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

type options struct {
	state, workspace, specialistTools, model, agents, profiles, profile, subagentSocket string
	maxConcurrent, maxSubagentDepth, maxSubagents, attemptRetention                     int
	idleTimeout                                                                         time.Duration
	isolation, subagents, showVersion                                                   bool
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.state, "state", "", "path to the state file (default: user config directory)")
	flag.StringVar(&o.workspace, "workspace", "", "working directory for new tasks (default: current directory)")
	flag.StringVar(&o.specialistTools, "specialist-tools", "", "path to bundled specialist tool limits (JSON)")
	flag.StringVar(&o.model, "model", "", "model for agents without a specialist model (provider/model); it takes precedence over a profile model")
	flag.StringVar(&o.agents, "agents", "", "path to optional agent command configuration JSON")
	flag.StringVar(&o.profiles, "profiles", "", "profile and specialist settings (default: profiles.json beside the state file)")
	flag.StringVar(&o.profile, "profile", "", "default profile for routed and unchosen tasks (built-in profile uses executor defaults)")
	flag.IntVar(&o.maxConcurrent, "max-concurrent", orchestrator.DefaultConcurrency, "maximum simultaneous agent processes")
	flag.DurationVar(&o.idleTimeout, "idle-timeout", 0, "cancel an agent that produces no output for this long (0 disables)")
	flag.BoolVar(&o.isolation, "isolation", true, "allow automatic Git worktrees for mandates that request changes")
	flag.BoolVar(&o.subagents, "subagents", true, "allow running agents to spawn child tasks")
	flag.StringVar(&o.subagentSocket, "subagent-socket", "", "path to the local subagent control socket (default: beside the state file)")
	flag.IntVar(&o.maxSubagentDepth, "max-subagent-depth", orchestrator.DefaultMaxSubagentDepth, "maximum subagent delegation depth")
	flag.IntVar(&o.maxSubagents, "max-subagents", orchestrator.DefaultMaxSubagents, "maximum child tasks one task may spawn")
	flag.IntVar(&o.attemptRetention, "attempt-retention", orchestrator.DefaultAttemptRetention, "attempt outputs kept in full per task (0 keeps every attempt)")
	flag.BoolVar(&o.showVersion, "version", false, "print the build version and exit")
	flag.Usage = usage
	flag.Parse()
	return o
}

func run() (err error) {
	o := parseFlags()
	if flag.NArg() != 0 {
		return fmt.Errorf("%w: unexpected arguments: %s", errUsage, strings.Join(flag.Args(), " "))
	}
	if o.showVersion {
		fmt.Println("pilharmonic " + versionString())
		return nil
	}
	if o.workspace == "" {
		if o.workspace, err = os.Getwd(); err != nil {
			return err
		}
	}
	if o.state == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		o.state = filepath.Join(dir, "orchestrator", "state.json")
	}
	release, err := orchestrator.AcquireInstanceLock(o.state)
	if err != nil {
		return err
	}
	defer release()
	service, err := orchestrator.Open(o.state)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, service.Shutdown()) }()
	if err = configure(service, o); err != nil {
		return err
	}
	return tui.Run(service, o.workspace)
}

// configure applies the command-line settings to an opened service.
func configure(service *orchestrator.Service, o options) error {
	if err := service.SetDefaultIsolation(o.isolation); err != nil {
		return err
	}
	if o.agents != "" {
		if err := service.LoadAgents(o.agents); err != nil {
			return err
		}
	}
	service.LoadBuiltinSpecialists()
	if o.specialistTools != "" {
		if err := service.LoadSpecialistTools(o.specialistTools); err != nil {
			return err
		}
	}
	profilesPath := o.profiles
	if profilesPath == "" {
		profilesPath = filepath.Join(filepath.Dir(o.state), "profiles.json")
	}
	if err := loadStartupProfiles(service, profilesPath, o.profiles != ""); err != nil {
		return err
	}
	if _, err := os.Stat(profilesPath); err == nil {
		fmt.Fprintf(os.Stderr, "pilharmonic: loaded profiles from %s\n", profilesPath)
	}
	if o.model != "" {
		service.SetDefaultModel(o.model)
	}
	if o.profile != "" {
		if err := service.SetRoutingProfile(o.profile); err != nil {
			return err
		}
	}
	return configureExecution(service, o)
}

func configureExecution(service *orchestrator.Service, o options) error {
	if !o.subagents {
		o.maxSubagentDepth, o.maxSubagents = 0, 0
	}
	if err := service.SetSubagentLimits(o.maxSubagentDepth, o.maxSubagents); err != nil {
		return err
	}
	if o.maxSubagentDepth > 0 && o.maxSubagents > 0 {
		path := o.subagentSocket
		if path == "" {
			path = filepath.Join(filepath.Dir(o.state), "subagent.sock")
		}
		if err := service.EnableSubagentControl(path); err != nil {
			return err
		}
	}
	return errors.Join(service.SetConcurrencyLimit(o.maxConcurrent), service.SetIdleTimeout(o.idleTimeout), service.SetAttemptRetention(o.attemptRetention))
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
