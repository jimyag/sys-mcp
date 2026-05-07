package cmdexec_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jimyag/sysplane/internal/sysplane-agent/cmdexec"
)

func TestRunProcessSuccess(t *testing.T) {
	command, args := shellCommand("printf 'hello'")
	out, err := cmdexec.New([]string{command}).Run(context.Background(), mustJSON(map[string]any{
		"command": command,
		"args":    args,
	}))
	if err != nil {
		t.Fatalf("RunProcess: %v", err)
	}

	var res cmdexec.RunProcessResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !res.Success || res.Stdout != "hello" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunProcessCapturesExitCode(t *testing.T) {
	command, args := shellCommand("printf 'boom' >&2; exit 7")
	out, err := cmdexec.New([]string{command}).Run(context.Background(), mustJSON(map[string]any{
		"command": command,
		"args":    args,
	}))
	if err != nil {
		t.Fatalf("RunProcess: %v", err)
	}

	var res cmdexec.RunProcessResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if res.Success || res.ExitCode != 7 {
		t.Fatalf("unexpected exit result: %+v", res)
	}
}

func TestRunProcessTimeout(t *testing.T) {
	command, args := shellCommand("sleep 2")
	_, err := cmdexec.New([]string{command}).Run(context.Background(), mustJSON(map[string]any{
		"command":     command,
		"args":        args,
		"timeout_sec": 1,
	}))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRunProcessDisabledByDefault(t *testing.T) {
	command, args := shellCommand("printf 'hello'")
	_, err := cmdexec.New(nil).Run(context.Background(), mustJSON(map[string]any{
		"command": command,
		"args":    args,
	}))
	if err == nil || err.Error() != "run_process: command execution is disabled on this agent" {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestRunProcessRejectsNonWhitelistedCommand(t *testing.T) {
	command, args := shellCommand("printf 'hello'")
	_, err := cmdexec.New([]string{"/bin/echo"}).Run(context.Background(), mustJSON(map[string]any{
		"command": command,
		"args":    args,
	}))
	if err == nil {
		t.Fatal("expected allowlist error")
	}
}

func shellCommand(script string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C", script}
	}
	return filepath.Clean("/bin/sh"), []string{"-c", script}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
