package tart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mountTag is the virtiofs tag every shared directory uses, so they all
// appear side by side under one mount point in the guest.
const mountTag = "act"

const (
	// runStopGrace is how long Stop waits for `tart run` after SIGINT.
	runStopGrace = 20 * time.Second
	// stopTimeout is passed to `tart stop --timeout`.
	stopTimeout = 10
)

// sharedDir is a host directory shared into the guest.
type sharedDir struct {
	Name string
	Path string
}

func (d sharedDir) arg() (string, error) {
	if d.Name == "" || strings.ContainsAny(d.Name, ":,/") {
		return "", fmt.Errorf("invalid shared directory name %q", d.Name)
	}
	if strings.Contains(d.Path, ":") {
		return "", fmt.Errorf("cannot share %q with tart: the path contains ':'", d.Path)
	}
	return fmt.Sprintf("%s:%s:tag=%s", d.Name, d.Path, mountTag), nil
}

// runOptions are the `tart run` flags that come from the platform options.
type runOptions struct {
	Headless     bool
	Softnet      bool
	SoftnetAllow string
}

// vm is one Tart VM.
type vm struct {
	cli  *cli
	name string

	mu      sync.Mutex
	runCmd  *exec.Cmd
	runDone chan struct{}
	runErr  error
}

func newVM(c *cli, name string) *vm {
	return &vm{cli: c, name: name}
}

// pull runs `tart pull <image>`.
func (c *cli) pull(ctx context.Context, image string) error {
	_, _, err := c.run(ctx, "pull", image)
	return err
}

// Clone runs `tart clone <image> <name>`, which also pulls a missing remote
// image.
func (v *vm) Clone(ctx context.Context, image string) error {
	_, _, err := v.cli.run(ctx, "clone", image, v.name)
	return err
}

// Set runs `tart set` when cpu or memory is set.
func (v *vm) Set(ctx context.Context, cpu, memoryMB int) error {
	args := []string{"set", v.name}
	if cpu > 0 {
		args = append(args, "--cpu", strconv.Itoa(cpu))
	}
	if memoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(memoryMB))
	}
	if len(args) == 2 {
		return nil
	}
	_, _, err := v.cli.run(ctx, args...)
	return err
}

func runArgs(name string, dirs []sharedDir, opts runOptions) ([]string, error) {
	args := []string{"run"}
	if opts.Headless {
		args = append(args, "--no-graphics")
	}
	if opts.SoftnetAllow != "" {
		args = append(args, "--net-softnet-allow="+opts.SoftnetAllow)
	} else if opts.Softnet {
		args = append(args, "--net-softnet")
	}
	for _, d := range dirs {
		a, err := d.arg()
		if err != nil {
			return nil, err
		}
		args = append(args, "--dir", a)
	}
	return append(args, name), nil
}

// Run starts `tart run` in the background. It is not tied to a caller's
// context: the VM outlives the call that starts it and is stopped by Stop.
func (v *vm) Run(dirs []sharedDir, opts runOptions, out io.Writer) error {
	args, err := runArgs(v.name, dirs, opts)
	if err != nil {
		return err
	}
	// Not tied to a caller context: the VM must outlive the call that starts it.
	cmd := v.cli.command(context.Background(), args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w (%s)", ErrTartNotFound, v.cli.bin)
		}
		return fmt.Errorf("cannot start `tart run`: %w", err)
	}
	done := make(chan struct{})
	v.mu.Lock()
	v.runCmd = cmd
	v.runDone = done
	v.mu.Unlock()
	go func() {
		err := cmd.Wait()
		v.mu.Lock()
		v.runErr = err
		v.mu.Unlock()
		close(done)
	}()
	return nil
}

// exited returns a channel closed when `tart run` exits, or nil when it was
// never started.
func (v *vm) exited() <-chan struct{} {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.runDone
}

func (v *vm) runExitErr() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.runErr != nil {
		return fmt.Errorf("`tart run` exited: %w", v.runErr)
	}
	return errors.New("`tart run` exited")
}

// IP runs `tart ip`, waiting up to wait seconds for the VM to boot. It
// fails early if `tart run` exits meanwhile.
func (v *vm) IP(ctx context.Context, resolver string, wait int) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if done := v.exited(); done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	args := []string{"ip", v.name, "--wait", strconv.Itoa(wait)}
	if resolver != "" {
		args = append(args, "--resolver", resolver)
	}
	out, _, err := v.cli.run(ctx, args...)
	if err != nil {
		// A VM that failed to boot makes `tart ip` fail too; prefer the
		// clearer error when `tart run` exits shortly after.
		if done := v.exited(); done != nil {
			select {
			case <-done:
				return "", v.runExitErr()
			case <-time.After(2 * time.Second):
			}
		}
		return "", err
	}
	ip := firstLine(out)
	if ip == "" {
		return "", fmt.Errorf("tart ip printed no address for %s", v.name)
	}
	return ip, nil
}

// Stop shuts the VM down: SIGINT to `tart run`, then `tart stop` if it has
// not exited within runStopGrace. It returns once `tart run` has exited.
func (v *vm) Stop(ctx context.Context) error {
	v.mu.Lock()
	cmd, done := v.runCmd, v.runDone
	v.mu.Unlock()
	if cmd == nil {
		_, _, err := v.cli.run(ctx, "stop", v.name, "--timeout", strconv.Itoa(stopTimeout))
		return err
	}
	select {
	case <-done:
		return nil
	default:
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
		return nil
	case <-time.After(runStopGrace):
	case <-ctx.Done():
	}
	_, _, stopErr := v.cli.run(context.WithoutCancel(ctx), "stop", v.name, "--timeout", strconv.Itoa(stopTimeout))
	select {
	case <-done:
		return nil
	case <-time.After(time.Duration(stopTimeout+5) * time.Second):
	}
	_ = cmd.Process.Kill()
	<-done
	if stopErr != nil {
		return stopErr
	}
	return nil
}

// Delete runs `tart delete <name>`.
func (v *vm) Delete(ctx context.Context) error {
	_, _, err := v.cli.run(ctx, "delete", v.name)
	return err
}
