package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var completionRecord = regexp.MustCompile(`^1 (prepare|run) [A-Za-z0-9]{1,128}\n$`)

// execute runs the CLI beside its bundled libraries. Cancellation terminates
// its process group, with a bounded wait and forced cleanup of descendants.
func execute(ctx context.Context, executable string, args, env []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := filepath.Abs(executable)
	if err != nil {
		return errors.New("could not resolve the HammerDB executable")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return ctx.Err()
	}
	return err
}

// checkCompletion accepts only the small record written by runner_complete.
// The path belongs to a newly staged private directory for this invocation.
func checkCompletion(path, phase string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("missing or invalid phase completion record")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("could not read phase completion record")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 161))
	if err != nil || !completionRecord.Match(data) || !strings.HasPrefix(string(data), "1 "+phase+" ") {
		return errors.New("invalid phase completion record")
	}
	return nil
}
