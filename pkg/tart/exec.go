package tart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nektos/act/pkg/common"
)

var errNotStarted = errors.New("tart: the VM is not running")

func (e *Environment) currentStdout() io.Writer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stdout
}

func (e *Environment) currentTransport() transport {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.transport
}

// stepEnv overlays the step environment on the captured login environment,
// so steps see the guest's PATH (Homebrew and so on) like an interactive
// login would.
func (e *Environment) stepEnv(env map[string]string) map[string]string {
	e.mu.Lock()
	login := e.loginEnv
	e.mu.Unlock()
	merged := filterLoginEnv(login)
	for k, v := range env {
		merged[k] = v
	}
	return merged
}

// Exec runs command in the guest. The user argument is ignored, as on the
// host backend: steps run as the SSH user.
func (e *Environment) Exec(command []string, env map[string]string, _, workdir string) common.Executor {
	return func(ctx context.Context) error {
		if err := e.exec(ctx, command, env, workdir); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("this step has been cancelled: %w", err)
			}
			return err
		}
		return nil
	}
}

func (e *Environment) exec(ctx context.Context, command []string, env map[string]string, workdir string) error {
	t := e.currentTransport()
	if t == nil {
		return errNotStarted
	}
	if len(command) == 0 {
		return errors.New("tart: empty command")
	}
	wd := resolveWorkdir(e.paths.workspace, workdir)
	l, err := writeLauncher(e.dirs.Act, e.paths.actPath(), wd, e.stepEnv(env))
	if err != nil {
		return err
	}
	defer l.cleanup()
	common.Logger(ctx).Debugf("tart: exec %q in %s", command, wd)

	// The kill runs after ctx is cancelled, so it gets a fresh context.
	stop := context.AfterFunc(ctx, func() { e.killGroup(t, l.pgidGuest) }) //nolint:contextcheck // ctx is already done here
	defer stop()

	argv := append([]string{"/bin/sh", l.guestPath}, command...)
	code, err := t.Run(ctx, argv, e.currentStdout())
	if err != nil {
		return err
	}
	return exitCodeError(code)
}

// killGroup kills the process tree of a cancelled command. Neither an SSH
// signal request nor closing the session reaches the command's children,
// so this runs a second command that kills the recorded process group.
func (e *Environment) killGroup(t transport, pgidFile string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = t.Run(ctx, []string{"/bin/sh", "-c", killScript(pgidFile)}, io.Discard)
}

// exitCodeError uses the same messages as the Docker backend.
func exitCodeError(code int) error {
	switch code {
	case 0:
		return nil
	case 127:
		return fmt.Errorf("exitcode '%d': command not found, please refer to https://github.com/nektos/act/issues/107 for more information", code)
	default:
		return fmt.Errorf("exitcode '%d': failure", code)
	}
}
