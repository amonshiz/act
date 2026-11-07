package tart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// agentReadyWait is how long Start waits for the Tart Guest Agent before
// falling back to SSH.
var agentReadyWait = 60 * time.Second

// agentTransport runs commands with `tart exec`, which talks to the Tart
// Guest Agent in the VM. The exit code is tart exec's own, which is the
// command's.
type agentTransport struct {
	cli *cli
	vm  string
}

func (a *agentTransport) Run(ctx context.Context, argv []string, out io.Writer) (int, error) {
	// Not tied to ctx: on cancel the caller kills the guest process group
	// first, then this waits briefly before killing the local tart exec.
	cmd := a.cli.command(context.WithoutCancel(ctx), append([]string{"exec", a.vm}, argv...)...)
	w := &lockedWriter{w: out}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("cannot start `tart exec`: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return agentExit(err)
	case <-ctx.Done():
	}
	select {
	case err := <-done:
		code, _ := agentExit(err)
		return code, ctx.Err()
	case <-time.After(cancelGrace):
		_ = cmd.Process.Kill()
		<-done
		return -1, ctx.Err()
	}
}

func agentExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return ee.ExitCode(), nil
	}
	return -1, fmt.Errorf("`tart exec` failed: %w", err)
}

func (a *agentTransport) Close() error { return nil }

// probeAgent waits until `tart exec <vm> true` succeeds, `tart run` exits,
// or agentReadyWait passes.
func probeAgent(ctx context.Context, c *cli, v *vm) error {
	ctx, cancel := context.WithTimeout(ctx, agentReadyWait)
	defer cancel()
	ctx, stop := cancelOnExit(ctx, v)
	defer stop()
	var lastErr error
	for {
		actx, acancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, err := c.run(actx, "exec", v.name, "true")
		acancel()
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(time.Second):
		}
	}
}
