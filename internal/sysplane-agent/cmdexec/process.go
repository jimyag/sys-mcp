package cmdexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultMaxOutputBytes = 256 * 1024

type RunProcessParams struct {
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	Dir            string   `json:"dir,omitempty"`
	TimeoutSec     int      `json:"timeout_sec,omitempty"`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty"`
}

type RunProcessResult struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	ExitCode   int      `json:"exit_code"`
	Success    bool     `json:"success"`
	Truncated  bool     `json:"truncated"`
	DurationMs int64    `json:"duration_ms"`
}

type Runner struct {
	allowed map[string]struct{}
}

func New(allowedCommands []string) *Runner {
	allowed := make(map[string]struct{}, len(allowedCommands))
	for _, command := range allowedCommands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		allowed[filepath.Clean(command)] = struct{}{}
	}
	return &Runner{allowed: allowed}
}

func RunProcess(ctx context.Context, argsJSON string) (string, error) {
	return New(nil).Run(ctx, argsJSON)
}

func (r *Runner) Run(ctx context.Context, argsJSON string) (string, error) {
	var params RunProcessParams
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return "", fmt.Errorf("run_process: invalid args: %w", err)
	}

	command := filepath.Clean(strings.TrimSpace(params.Command))
	if command == "" || command == "." {
		return "", fmt.Errorf("run_process: command is required")
	}
	if !filepath.IsAbs(command) {
		return "", fmt.Errorf("run_process: command must be an absolute path")
	}
	if len(r.allowed) == 0 {
		return "", fmt.Errorf("run_process: command execution is disabled on this agent")
	}
	if _, ok := r.allowed[command]; !ok {
		return "", fmt.Errorf("run_process: command %q is not in security.allowed_commands", command)
	}
	params.Command = command

	if params.MaxOutputBytes <= 0 {
		params.MaxOutputBytes = defaultMaxOutputBytes
	}
	if params.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(params.TimeoutSec)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, params.Command, params.Args...)
	cmd.Dir = params.Dir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("run_process: stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("run_process: stderr pipe: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("run_process: start %s: %w", params.Command, err)
	}

	stdoutBuf := &limitBuffer{limit: params.MaxOutputBytes}
	stderrBuf := &limitBuffer{limit: params.MaxOutputBytes}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdoutBuf, stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stderrBuf, stderrPipe)
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	exitCode := 0
	success := true
	if waitErr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		success = false
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", fmt.Errorf("run_process: wait %s: %w", params.Command, waitErr)
		}
	}

	res := RunProcessResult{
		Command:    params.Command,
		Args:       append([]string(nil), params.Args...),
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   exitCode,
		Success:    success,
		Truncated:  stdoutBuf.Truncated() || stderrBuf.Truncated(),
		DurationMs: time.Since(start).Milliseconds(),
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

type limitBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(p) > remaining {
			b.buf = append(b.buf, p[:remaining]...)
			b.truncated = true
			return len(p), nil
		}
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	b.truncated = true
	return len(p), nil
}

func (b *limitBuffer) String() string {
	return string(b.buf)
}

func (b *limitBuffer) Truncated() bool {
	return b.truncated
}
