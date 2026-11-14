package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"pilharmonic/internal/instance"
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
	orchestrator.Options
	state, workspace string
	showVersion      bool
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.state, "state", "", "path to the state file (default: user config directory)")
	flag.StringVar(&o.workspace, "workspace", "", "working directory for new tasks (default: current directory)")
	flag.StringVar(&o.DefaultModel, "model", "", "fallback model (provider/model) when no profile, class or specialist model applies")
	flag.StringVar(&o.AgentsPath, "agents", "", "path to optional agent command configuration JSON")
	flag.StringVar(&o.ProfilesPath, "profiles", "", "profile and specialist settings (default: profiles.json beside the state file)")
	flag.StringVar(&o.RoutingProfile, "profile", "", "default profile for routed and unchosen tasks (built-in profile uses executor defaults)")
	flag.IntVar(&o.MaxConcurrent, "max-concurrent", orchestrator.DefaultConcurrency, "maximum simultaneous agent processes")
	flag.DurationVar(&o.IdleTimeout, "idle-timeout", 0, "cancel an agent that produces no output for this long (0 disables)")
	flag.BoolVar(&o.Isolation, "isolation", true, "allow automatic Git worktrees for mandates that request changes")
	flag.BoolVar(&o.Subagents, "subagents", true, "allow running agents to spawn child tasks")
	flag.StringVar(&o.SubagentSocket, "subagent-socket", "", "path to the local subagent control socket (default: beside the state file)")
	flag.IntVar(&o.MaxSubagentDepth, "max-subagent-depth", orchestrator.DefaultMaxSubagentDepth, "maximum subagent delegation depth")
	flag.IntVar(&o.MaxSubagents, "max-subagents", orchestrator.DefaultMaxSubagents, "maximum child tasks one task may spawn")
	flag.IntVar(&o.AttemptRetention, "attempt-retention", orchestrator.DefaultAttemptRetention, "attempt outputs kept in full per task (0 keeps every attempt)")
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
		if o.state, err = orchestrator.DefaultStatePath(); err != nil {
			return err
		}
	}
	release, err := instance.Acquire(o.state)
	if err != nil {
		return err
	}
	defer release()
	service, err := orchestrator.Open(o.state)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, service.Shutdown()) }()
	profiles, err := service.Configure(o.Options)
	if err != nil {
		return err
	}
	if profiles != "" {
		fmt.Fprintf(os.Stderr, "pilharmonic: loaded profiles from %s\n", profiles)
	}
	return tui.Run(service, o.workspace)
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "Usage:\n  pilharmonic [flags]\n  pilharmonic subagent [flags]\n\nFlags:\n")
	flag.PrintDefaults()
}
