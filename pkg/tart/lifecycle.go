package tart

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/container"
)

const (
	// hostVMLimit is how many macOS VMs Apple allows per host.
	hostVMLimit = 2
	// ipWaitSeconds is passed to `tart ip --wait`.
	ipWaitSeconds = 120
	setupTimeout  = 2 * time.Minute
	// teardownTimeout keeps teardown inside the minute act allows for
	// removing a job container.
	teardownTimeout = 55 * time.Second
	// envMarker separates the facts the setup script prints from the
	// NUL-separated login environment that follows.
	envMarker = "\x00__ACT_LOGIN_ENV__\x00"
)

// sshReadyWait is how long Start waits for sshd in the guest.
var sshReadyWait = 2 * time.Minute

type lifecycleState int

const (
	stateNew lifecycleState = iota
	stateRunning
	stateDone
)

// guestFacts are read from the guest during setup.
type guestFacts struct {
	arch      string // uname -m
	osVersion string // sw_vers -productVersion
	node      string // command -v node in a login shell
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Pull is a no-op; the image is pulled by `tart clone` in Start.
func (e *Environment) Pull(bool) common.Executor { return errorExecutor(nil) }

// Create is a no-op; the VM is cloned in Start.
func (e *Environment) Create([]string, []string) common.Executor { return errorExecutor(nil) }

// Start clones and boots the VM, connects to it and prepares the guest. On
// failure it undoes everything it did before returning, because act does
// not call Remove after a failed start.
func (e *Environment) Start(bool) common.Executor {
	return func(ctx context.Context) error {
		err := e.start(ctx)
		if err != nil {
			e.finish.Do(func() { e.teardown(ctx) })
		}
		return err
	}
}

func (e *Environment) checkPlatform() error {
	if !e.cfg.SkipPlatformCheck && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") {
		return fmt.Errorf("tart platforms need macOS on Apple Silicon; this host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return e.cli.lookPath()
}

func (e *Environment) start(ctx context.Context) error {
	logger := common.Logger(ctx)
	if err := e.checkPlatform(); err != nil {
		return err
	}

	if len(e.limiter.slots) == cap(e.limiter.slots) {
		logger.Infof("  ⏳  Waiting for a free Tart VM slot (--tart-max-vms=%d)", cap(e.limiter.slots))
	}
	s, err := e.limiter.Acquire(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.slot = s
	e.mu.Unlock()

	if err := e.prepareHost(ctx); err != nil {
		return err
	}

	v := newVM(e.cli, e.name+"-"+randomSuffix())
	e.mu.Lock()
	e.vm = v
	e.mu.Unlock()
	if e.opts.Pull {
		logger.Infof("  ☁  tart pull %s", e.opts.Image)
		if err := e.cli.pull(ctx, e.opts.Image); err != nil {
			return err
		}
	}
	logger.Infof("  ☁  tart clone %s %s", e.opts.Image, v.name)
	if err := v.Clone(ctx, e.opts.Image); err != nil {
		return err
	}
	e.mu.Lock()
	e.cloned = true
	e.mu.Unlock()
	if err := v.Set(ctx, e.opts.CPU, e.opts.MemoryMB); err != nil {
		return err
	}

	// `tart run` must outlive ctx, which ends when the job's setup step returns.
	runOpts := runOptions{Headless: e.opts.Headless, Softnet: e.opts.Softnet, SoftnetAllow: e.opts.SoftnetAllow}
	if err := v.Run(e.paths.shares, runOpts, e.runLog); err != nil { //nolint:contextcheck // see above
		return err
	}
	e.mu.Lock()
	e.state = stateRunning
	e.mu.Unlock()

	if err := e.connect(ctx, v); err != nil {
		return err
	}
	return e.setupGuest(ctx)
}

// prepareHost fails fast when other processes already run the maximum
// number of VMs, and deletes stopped VMs left over from earlier runs of
// this job. Running VMs are never touched: they may belong to another act
// process.
func (e *Environment) prepareHost(ctx context.Context) error {
	logger := common.Logger(ctx)
	vms, err := e.cli.listVMs(ctx)
	if err != nil {
		return err
	}
	running := 0
	for _, v := range vms {
		if v.isRunning() {
			running++
			continue
		}
		if e.name != "" && strings.HasPrefix(v.Name, e.name+"-") {
			logger.Infof("  \U0001f9f9  Deleting stopped VM %s left over from an earlier run", v.Name)
			if err := newVM(e.cli, v.Name).Delete(ctx); err != nil {
				logger.Warnf("Cannot delete leftover VM %s: %v", v.Name, err)
			}
		}
	}
	if running >= hostVMLimit {
		return fmt.Errorf("this host already runs %d Tart VMs and macOS allows %d at a time; stop one (tart list, tart stop <name>) and retry", running, hostVMLimit)
	}
	return nil
}

func (e *Environment) connect(ctx context.Context, v *vm) error {
	logger := common.Logger(ctx)
	if e.opts.Transport == TransportAgent {
		err := probeAgent(ctx, e.cli, v)
		if err == nil {
			logger.Debugf("tart: using tart exec for VM %s", v.name)
			e.mu.Lock()
			e.transport = &agentTransport{cli: e.cli, vm: v.name}
			e.mu.Unlock()
			return nil
		}
		if done := v.exited(); done != nil {
			select {
			case <-done:
				return v.runExitErr()
			default:
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warnf("The Tart Guest Agent in VM %s did not answer (%v); falling back to SSH", v.name, err)
	}
	ip, err := v.IP(ctx, e.opts.Resolver, ipWaitSeconds)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(e.cfg.SSHPort))
	logger.Debugf("tart: VM %s is at %s", v.name, addr)
	config, err := sshClientConfig(e.cfg)
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, sshReadyWait)
	defer cancel()
	dctx, stop := cancelOnExit(dctx, v)
	defer stop()
	t, err := newSSHTransport(dctx, addr, config)
	if err != nil {
		if done := v.exited(); done != nil {
			select {
			case <-done:
				return v.runExitErr()
			default:
			}
		}
		return err
	}
	e.mu.Lock()
	e.transport = t
	e.mu.Unlock()
	return nil
}

// cancelOnExit cancels ctx when `tart run` exits.
func cancelOnExit(ctx context.Context, v *vm) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := v.exited()
	if done == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// setupScript mounts the shares, creates the tool cache, and prints guest
// facts followed by the login environment.
func (e *Environment) setupScript() string {
	mp := shellquote.Join(e.paths.mount)
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "mp=%s\n", mp)
	if !e.cfg.SkipMount {
		fmt.Fprintf(&b, "mkdir -p \"$mp\"\nif ! mount | grep -qF \" on $mp \"; then /sbin/mount_virtiofs %s \"$mp\"; fi\n", mountTag)
	}
	b.WriteString(`i=0
while [ ! -d "$mp/act" ] && [ $i -lt 100 ]; do sleep 0.1; i=$((i+1)); done
[ -d "$mp/act" ] || { echo "the shared directory $mp/act did not appear in the VM" >&2; exit 1; }
`)
	fmt.Fprintf(&b, "mkdir -p %s\n", shellquote.Join(e.paths.toolCache()))
	sh := shellquote.Join(e.cfg.LoginShell)
	b.WriteString("echo \"arch=$(uname -m)\"\n")
	b.WriteString("echo \"version=$(sw_vers -productVersion 2>/dev/null || true)\"\n")
	fmt.Fprintf(&b, "echo \"node=$(%s -l -c 'command -v node' </dev/null 2>/dev/null || true)\"\n", sh)
	fmt.Fprintf(&b, "printf '%s'\n", strings.ReplaceAll(envMarker, "\x00", `\000`))
	fmt.Fprintf(&b, "%s -l -c 'env -0' </dev/null\n", sh)
	return b.String()
}

func parseSetupOutput(out string) (guestFacts, map[string]string, error) {
	var facts guestFacts
	head, envPart, ok := strings.Cut(out, envMarker)
	if !ok {
		return facts, nil, errors.New("the VM setup script printed no environment")
	}
	for _, line := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "arch":
			facts.arch = v
		case "version":
			facts.osVersion = v
		case "node":
			facts.node = v
		}
	}
	env := map[string]string{}
	for _, kv := range strings.Split(envPart, "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	return facts, env, nil
}

func (e *Environment) setupGuest(ctx context.Context) error {
	logger := common.Logger(ctx)
	t := e.currentTransport()
	sctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	var out bytes.Buffer
	code, err := t.Run(sctx, []string{"/bin/sh", "-c", e.setupScript()}, &out)
	if err != nil {
		return fmt.Errorf("VM setup failed: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("VM setup failed with exit code %d: %s", code, firstLine(lastLines(out.String())))
	}
	facts, env, err := parseSetupOutput(out.String())
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.guest = facts
	e.loginEnv = env
	e.mu.Unlock()
	logger.Infof("  \U0001f5a5  VM %s is ready (macOS %s, %s)", e.vm.name, facts.osVersion, facts.arch)
	if facts.node == "" {
		logger.Warnf("node was not found in the VM; JavaScript actions will fail. Use an image that includes Node.js.")
	}
	return nil
}

func lastLines(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Remove tears the VM down. act calls it when the job succeeded or with
// --rm.
func (e *Environment) Remove() common.Executor {
	return func(ctx context.Context) error {
		e.finish.Do(func() { e.teardown(ctx) })
		return nil
	}
}

// Close is always called at the end of a job. It tears the VM down unless
// the job failed (and was not cancelled) with --tart-keep-failed set; then
// it keeps the VM, logs how to remove it, and frees the slot.
func (e *Environment) Close() common.Executor {
	return func(ctx context.Context) error {
		jobErr := common.JobError(ctx)
		cancelled := ctx.Err() != nil || errors.Is(jobErr, context.Canceled)
		keep := e.cfg.KeepFailed && jobErr != nil && !cancelled
		e.finish.Do(func() {
			if keep {
				e.keep(ctx)
			} else {
				e.teardown(ctx)
			}
		})
		return nil
	}
}

func (e *Environment) keep(ctx context.Context) {
	e.mu.Lock()
	t, v, s := e.transport, e.vm, e.slot
	e.transport = nil
	e.state = stateDone
	e.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
	if v != nil {
		common.Logger(ctx).Warnf("Keeping VM %s of the failed job (--tart-keep-failed). Remove it with: tart stop %s && tart delete %s && rm -rf %s",
			v.name, v.name, v.name, shellquote.Join(e.dirs.Root))
	}
	s.Release()
}

// teardown stops and deletes the VM, removes the job directory and frees
// the slot. It runs on its own context so a cancelled job still cleans up.
func (e *Environment) teardown(ctx context.Context) {
	logger := common.Logger(ctx)
	tctx, cancel := context.WithTimeout(common.WithLogger(context.WithoutCancel(ctx), logger), teardownTimeout)
	defer cancel()

	e.mu.Lock()
	t, v, cloned, s, state := e.transport, e.vm, e.cloned, e.slot, e.state
	e.transport = nil
	e.state = stateDone
	e.mu.Unlock()

	if t != nil {
		_ = t.Close()
	}
	if v != nil && state == stateRunning {
		if err := v.Stop(tctx); err != nil {
			logger.Warnf("Cannot stop VM %s: %v", v.name, err)
		}
	}
	if v != nil && cloned {
		if err := v.Delete(tctx); err != nil {
			logger.Errorf("Cannot delete VM %s: %v; remove it with: tart delete %s", v.name, err, v.name)
		} else {
			logger.Debugf("tart: deleted VM %s", v.name)
		}
	}
	if e.dirs.Root != "" {
		if err := os.RemoveAll(e.dirs.Root); err != nil {
			logger.Warnf("Cannot remove %s: %v", e.dirs.Root, err)
		}
	}
	s.Release()
}

// VMName returns the name of the VM once Start has created it.
func (e *Environment) VMName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.vm == nil {
		return ""
	}
	return e.vm.name
}

// UpdateFromImageEnv adds the guest's login environment, as the Docker
// backend adds the image's environment.
func (e *Environment) UpdateFromImageEnv(env *map[string]string) common.Executor {
	return func(context.Context) error {
		e.mu.Lock()
		login := filterLoginEnv(e.loginEnv)
		e.mu.Unlock()
		m := *env
		for k, v := range login {
			if k == "PATH" && m[k] != "" {
				m[k] += ":" + v
			} else if m[k] == "" {
				m[k] = v
			}
		}
		return nil
	}
}

// DefaultPathVariable is the guest login PATH, or a macOS default.
func (e *Environment) DefaultPathVariable() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.loginEnv["PATH"]; p != "" {
		return p
	}
	return defaultPath
}

func guestArchToActionArch(arch string) string {
	switch arch {
	case "arm64", "aarch64", "":
		return "ARM64"
	case "x86_64", "amd64":
		return "X64"
	}
	return arch
}

func (e *Environment) GetRunnerContext(context.Context) map[string]interface{} {
	e.mu.Lock()
	arch := e.guest.arch
	e.mu.Unlock()
	return map[string]interface{}{
		"os":         "macOS",
		"arch":       guestArchToActionArch(arch),
		"temp":       e.paths.tmpPath(),
		"tool_cache": e.paths.toolCache(),
	}
}

// ImageOS is the GitHub ImageOS value for the guest, such as macos26.
func (e *Environment) ImageOS() string {
	e.mu.Lock()
	v := e.guest.osVersion
	e.mu.Unlock()
	major, _, _ := strings.Cut(v, ".")
	if major == "" {
		return ""
	}
	return "macos" + major
}

func (e *Environment) GetHealth(context.Context) container.Health {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.state == stateDone:
		return container.HealthUnHealthy
	case e.transport != nil && e.loginEnv != nil:
		return container.HealthHealthy
	default:
		return container.HealthStarting
	}
}

func (e *Environment) ReplaceLogWriter(stdout, _ io.Writer) (io.Writer, io.Writer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	org := e.stdout
	e.stdout = stdout
	return org, org
}
