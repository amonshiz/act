// Package tart runs act jobs in throwaway macOS virtual machines managed by
// the Tart CLI (https://tart.run).
package tart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
)

var (
	// ErrTartNotFound is returned when the tart binary cannot be found.
	ErrTartNotFound = errors.New("tart was not found on PATH; install it with `brew install openai/tools/tart`")
	// ErrTartFailed is returned when a tart command exits unsuccessfully.
	ErrTartFailed = errors.New("tart command failed")
)

// cliError describes a failed tart invocation. It matches ErrTartFailed
// with errors.Is and carries the first line tart printed on stderr.
type cliError struct {
	subcommand string
	stderr     string
	err        error
}

func (e *cliError) Error() string {
	msg := fmt.Sprintf("tart %s failed", e.subcommand)
	if e.stderr != "" {
		return msg + ": " + e.stderr
	}
	if e.err != nil {
		return msg + ": " + e.err.Error()
	}
	return msg
}

func (e *cliError) Unwrap() []error {
	return []error{ErrTartFailed, e.err}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// cli runs the tart binary.
type cli struct {
	bin string
}

func newCLI(bin string) *cli {
	if bin == "" {
		bin = "tart"
	}
	return &cli{bin: bin}
}

// lookPath reports ErrTartNotFound when the binary is missing.
func (c *cli) lookPath() error {
	if _, err := exec.LookPath(c.bin); err != nil {
		return fmt.Errorf("%w (%s)", ErrTartNotFound, c.bin)
	}
	return nil
}

func (c *cli) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.bin, args...) //nolint:gosec // runs the configured tart binary
	cmd.SysProcAttr = newSessionAttr()
	return cmd
}

// run runs tart with args and returns its stdout and stderr.
func (c *cli) run(ctx context.Context, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := c.command(ctx, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return stdout.String(), stderr.String(), fmt.Errorf("%w (%s)", ErrTartNotFound, c.bin)
		}
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return stdout.String(), stderr.String(), &cliError{subcommand: sub, stderr: firstLine(stderr.String()), err: err}
	}
	return stdout.String(), stderr.String(), nil
}

// vmInfo is one entry of `tart list --format json`. The field names are
// not part of Tart's --help, so they are kept in this one struct.
type vmInfo struct {
	Name    string `json:"Name"`
	State   string `json:"State"`
	Running bool   `json:"Running"`
	Source  string `json:"Source"`
}

func (v vmInfo) isRunning() bool {
	return v.Running || strings.EqualFold(v.State, "running")
}

// listVMs returns the local VMs.
func (c *cli) listVMs(ctx context.Context) ([]vmInfo, error) {
	out, _, err := c.run(ctx, "list", "--source", "local", "--format", "json")
	if err != nil {
		return nil, err
	}
	var vms []vmInfo
	if strings.TrimSpace(out) == "" {
		return vms, nil
	}
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		return nil, fmt.Errorf("cannot parse `tart list` output: %w", err)
	}
	return vms, nil
}
