package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxControlRequest   = 4 << 20
	maxSubagentPrompt   = 256 << 10
	controlDialTimeout  = 2 * time.Second
	defaultSubagentWait = 30 * time.Minute
	defaultStartTimeout = 5 * time.Second
)

type subagentRequest struct {
	TaskID         string `json:"task_id"`
	Token          string `json:"token"`
	Title          string `json:"title,omitempty"`
	Prompt         string `json:"prompt"`
	Agent          string `json:"agent,omitempty"`
	Wait           bool   `json:"wait"`
	StartTimeoutMS int64  `json:"start_timeout_ms,omitempty"`
	TimeoutMS      int64  `json:"timeout_ms,omitempty"`
}

type subagentResponse struct {
	TaskID string `json:"task_id,omitempty"`
	Status string `json:"status,omitempty"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
	Note   string `json:"note,omitempty"`
}

// EnableSubagentControl starts the local Unix-socket endpoint used by running
// tasks to spawn children. The socket is owner-only and credentials are scoped
// to one attempt. It is separate from state persistence and may be disabled.
func (s *Service) EnableSubagentControl(path string) error {
	if path == "" {
		return errors.New("subagent control socket path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("subagent control path exists and is not a socket")
		}
		conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return errors.New("subagent control socket is already in use")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		_ = listener.Close()
		_ = os.Remove(path)
		return errors.New("service is shutting down")
	}
	if s.controlListener != nil {
		s.mu.Unlock()
		cancel()
		_ = listener.Close()
		_ = os.Remove(path)
		return errors.New("subagent control is already enabled")
	}
	s.controlPath = path
	s.controlListener = listener
	s.controlCancel = cancel
	s.controlWG.Add(1)
	s.mu.Unlock()

	go s.acceptSubagentControl(ctx, listener)
	return nil
}

func (s *Service) acceptSubagentControl(ctx context.Context, listener net.Listener) {
	defer s.controlWG.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.controlConns[conn] = struct{}{}
		s.controlWG.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.controlWG.Done()
			s.handleSubagentControl(ctx, conn)
			_ = conn.Close()
			s.mu.Lock()
			delete(s.controlConns, conn)
			s.mu.Unlock()
		}()
	}
}

func (s *Service) closeSubagentControl() {
	s.mu.Lock()
	listener, path, cancel := s.controlListener, s.controlPath, s.controlCancel
	s.controlListener, s.controlPath, s.controlCancel = nil, "", nil
	if cancel != nil {
		cancel()
	}
	for conn := range s.controlConns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	s.controlWG.Wait()
	if path != "" {
		_ = os.Remove(path)
	}
}

func (s *Service) handleSubagentControl(ctx context.Context, conn net.Conn) {
	decoder := json.NewDecoder(io.LimitReader(conn, maxControlRequest+1))
	decoder.DisallowUnknownFields()
	var req subagentRequest
	if err := decoder.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(subagentResponse{Error: "invalid request: " + err.Error()})
		return
	}
	if req.TaskID == "" || req.Token == "" {
		_ = json.NewEncoder(conn).Encode(subagentResponse{Error: "task ID and token are required"})
		return
	}
	if len(req.Prompt) > maxSubagentPrompt {
		_ = json.NewEncoder(conn).Encode(subagentResponse{Error: "subagent mandate exceeds 256 KiB"})
		return
	}
	if len(req.Title) > 4096 {
		_ = json.NewEncoder(conn).Encode(subagentResponse{Error: "subagent title is too long"})
		return
	}
	startTimeout := time.Duration(req.StartTimeoutMS) * time.Millisecond
	if startTimeout <= 0 {
		startTimeout = defaultStartTimeout
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultSubagentWait
	}
	if timeout > 2*time.Hour {
		timeout = 2 * time.Hour
	}
	task, err := s.spawnSubagent(req.TaskID, req.Title, req.Prompt, req.Agent, req.Token)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(subagentResponse{TaskID: task.ID, Error: err.Error()})
		return
	}
	response := subagentResponse{TaskID: task.ID, Status: string(task.Status), Output: task.Output, Error: task.Error}
	if req.Wait {
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result, waitErr := s.waitForSubagent(waitCtx, task.ID, startTimeout)
		response.Status = string(result.Status)
		response.Output = result.Output
		response.Error = result.Error
		if waitErr != nil {
			response.Error = waitErr.Error()
		}
	}
	if response.Status == string(Queued) {
		response.Note = "queued for agent capacity; the child continues independently"
	}
	_ = json.NewEncoder(conn).Encode(response)
}

// RunSubagentCommand implements the `orchestrator subagent` helper used by a
// running agent. It reads the child mandate from stdin and speaks only to the
// local control socket named in the environment. It never executes agent output.
func RunSubagentCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("subagent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	title := flags.String("title", "", "short display title for the child task")
	agent := flags.String("agent", "", "executor for the child (default: automatic selection, then parent executor)")
	socket := flags.String("socket", "", "control socket (default: ORCHESTRATOR_CONTROL_SOCKET)")
	noWait := flags.Bool("no-wait", false, "record and start the child without waiting for it")
	startTimeout := flags.Duration("start-timeout", defaultStartTimeout, "how long to wait for a free execution slot")
	timeout := flags.Duration("timeout", defaultSubagentWait, "maximum time to wait for completion")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "subagent: unexpected arguments")
		return 2
	}
	prompt, err := io.ReadAll(io.LimitReader(stdin, maxSubagentPrompt+1))
	if err != nil {
		fmt.Fprintln(stderr, "subagent:", err)
		return 1
	}
	if len(prompt) > maxSubagentPrompt {
		fmt.Fprintln(stderr, "subagent: mandate exceeds 256 KiB")
		return 2
	}
	if strings.TrimSpace(string(prompt)) == "" {
		fmt.Fprintln(stderr, "subagent: mandate on stdin is required")
		return 2
	}
	if *socket == "" {
		*socket = os.Getenv("ORCHESTRATOR_CONTROL_SOCKET")
	}
	taskID := os.Getenv("ORCHESTRATOR_TASK_ID")
	token := os.Getenv("ORCHESTRATOR_SUBAGENT_TOKEN")
	if *socket == "" || taskID == "" || token == "" {
		fmt.Fprintln(stderr, "subagent: not running inside an orchestrator task with subagents enabled")
		return 2
	}
	conn, err := net.DialTimeout("unix", *socket, controlDialTimeout)
	if err != nil {
		fmt.Fprintln(stderr, "subagent:", err)
		return 1
	}
	defer conn.Close()
	waitDeadline := 10 * time.Second
	if !*noWait {
		waitDeadline = *timeout + *startTimeout + 10*time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(waitDeadline))
	req := subagentRequest{
		TaskID:         taskID,
		Token:          token,
		Title:          *title,
		Prompt:         string(prompt),
		Agent:          *agent,
		Wait:           !*noWait,
		StartTimeoutMS: startTimeout.Milliseconds(),
		TimeoutMS:      timeout.Milliseconds(),
	}
	if err = json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintln(stderr, "subagent:", err)
		return 1
	}
	var resp subagentResponse
	if err = json.NewDecoder(io.LimitReader(conn, maxControlRequest)).Decode(&resp); err != nil {
		fmt.Fprintln(stderr, "subagent:", err)
		return 1
	}
	if resp.Output != "" {
		fmt.Fprint(stdout, resp.Output)
		if !strings.HasSuffix(resp.Output, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if resp.Error != "" {
		label := resp.Status
		if label == "" {
			label = "request failed"
		}
		fmt.Fprintf(stderr, "subagent %s %s: %s\n", resp.TaskID, label, resp.Error)
		return 1
	}
	switch resp.Status {
	case string(Completed):
		if resp.Output == "" {
			fmt.Fprintf(stdout, "Subagent %s completed with no output.\n", resp.TaskID)
		}
		return 0
	case string(Queued):
		fmt.Fprintf(stdout, "Subagent %s queued (%s).\n", resp.TaskID, resp.Note)
		return 0
	default:
		fmt.Fprintf(stdout, "Subagent %s %s.\n", resp.TaskID, resp.Status)
		return 0
	}
}
