//go:build !windows

package tart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/tart/internal/sshtest"
)

type lifecycleFixture struct {
	fake *fakeTart
	srv  *sshtest.Server
	dirs JobDirs
	home string
	host HostConfig
	lim  *limiter
	out  *syncBuffer
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	f := &lifecycleFixture{fake: newFakeTart(t), srv: sshtest.New(t, "admin", "secret"), home: t.TempDir(), lim: newLimiter(2), out: &syncBuffer{}}
	t.Setenv("FAKE_TART_MOUNT", filepath.Join(f.home, "act"))
	root := filepath.Join(t.TempDir(), "job")
	f.dirs = JobDirs{Root: root, Act: filepath.Join(root, "act"), Work: filepath.Join(root, "hostexecutor"), Tmp: filepath.Join(root, "tmp"), Workdir: t.TempDir()}
	for _, d := range []string{f.dirs.Act, f.dirs.Work, f.dirs.Tmp} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	f.host = HostConfig{
		SSHUser: "admin", SSHPassword: "secret",
		TartBinary: f.fake.bin, SSHPort: f.srv.Port(), GuestRoot: f.home,
		LoginShell: "/bin/sh", SkipPlatformCheck: true, SkipMount: true,
	}
	return f
}

func (f *lifecycleFixture) env(t *testing.T, platform string) *Environment {
	t.Helper()
	e, err := New(Config{Platform: platform, Name: "act-job", Dirs: f.dirs, Host: f.host, Stdout: f.out})
	require.NoError(t, err)
	e.limiter = f.lim
	return e
}

func (f *lifecycleFixture) assertClean(t *testing.T) {
	t.Helper()
	assert.Empty(t, f.vms(t), "no VM is left")
	_, err := os.Stat(f.dirs.Root)
	assert.True(t, os.IsNotExist(err), "the job directory is removed")
	assert.Len(t, f.lim.slots, 0, "the slot is free")
}

func (f *lifecycleFixture) vms(t *testing.T) []string {
	entries, err := os.ReadDir(filepath.Join(f.fake.state, "vms"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestLifecycleStartExecRemove(t *testing.T) {
	f := newLifecycleFixture(t)
	e := f.env(t, "tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest")
	ctx := context.Background()

	require.NoError(t, e.Start(false)(ctx))
	name := e.VMName()
	assert.Regexp(t, `^act-job-[0-9a-f]{6}$`, name)
	assert.Equal(t, 1, len(f.lim.slots))
	assert.Equal(t, "Healthy", healthName(e))

	require.NoError(t, e.Exec([]string{"sh", "-c", `echo "$RUNNER_X" > out.txt`}, map[string]string{"RUNNER_X": "hi"}, "", "")(ctx))
	b, err := os.ReadFile(filepath.Join(f.dirs.Work, "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hi\n", string(b))

	rctx := e.GetRunnerContext(ctx)
	assert.Equal(t, "macOS", rctx["os"])
	assert.Equal(t, guestArchToActionArch(map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]), rctx["arch"])
	assert.Equal(t, f.home+"/act/tmp", rctx["temp"])
	assert.Equal(t, f.home+"/hostedtoolcache", rctx["tool_cache"])
	_, err = os.Stat(filepath.Join(f.home, "hostedtoolcache"))
	assert.NoError(t, err, "setup creates the tool cache")
	if runtime.GOOS == "darwin" {
		assert.Regexp(t, `^macos\d+$`, e.ImageOS())
	}
	imageEnv := map[string]string{}
	require.NoError(t, e.UpdateFromImageEnv(&imageEnv)(ctx))
	assert.NotEmpty(t, imageEnv["PATH"])
	assert.Equal(t, imageEnv["PATH"], e.DefaultPathVariable())
	assert.NotContains(t, imageEnv, "SHLVL")

	require.NoError(t, e.Remove()(ctx))
	require.NoError(t, e.Close()(ctx))
	f.assertClean(t)
	assert.Equal(t, "UnHealthy", healthName(e))

	work, act, tmp := f.dirs.Work, f.dirs.Act, f.dirs.Tmp
	assert.Equal(t, []string{
		"list --source local --format json",
		"clone ghcr.io/cirruslabs/macos-tahoe-xcode:latest " + name,
		"run --no-graphics --dir act:" + act + ":tag=act --dir work:" + work + ":tag=act --dir tmp:" + tmp + ":tag=act " + name,
		"ip " + name + " --wait 120 --resolver dhcp",
		"delete " + name,
	}, f.fake.calls(t))
}

func healthName(e *Environment) string {
	switch e.GetHealth(context.Background()) {
	case 1:
		return "Healthy"
	case 2:
		return "UnHealthy"
	default:
		return "Starting"
	}
}

func TestLifecycleOptionsAndPull(t *testing.T) {
	f := newLifecycleFixture(t)
	e := f.env(t, "tart://img?pull=1&cpu=4&memory=8192&headless=0&softnet=1&resolver=arp")
	ctx := context.Background()
	require.NoError(t, e.Start(false)(ctx))
	name := e.VMName()
	require.NoError(t, e.Remove()(ctx))
	calls := f.fake.calls(t)
	assert.Equal(t, "pull img", calls[1])
	assert.Equal(t, "clone img "+name, calls[2])
	assert.Equal(t, "set "+name+" --cpu 4 --memory 8192", calls[3])
	assert.Regexp(t, `^run --net-softnet --dir act:`, calls[4])
	assert.Equal(t, "ip "+name+" --wait 120 --resolver arp", calls[5])
	f.assertClean(t)
}

func TestLifecycleStartFailuresCleanUp(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		setup    func(t *testing.T, f *lifecycleFixture)
		wantErr  string
	}{
		{name: "list", setup: func(t *testing.T, _ *lifecycleFixture) { t.Setenv("FAKE_TART_FAIL", "list") }, wantErr: "tart list failed"},
		{name: "pull", platform: "tart://img?pull=1", setup: func(t *testing.T, _ *lifecycleFixture) { t.Setenv("FAKE_TART_FAIL", "pull") }, wantErr: "tart pull failed"},
		{name: "clone", setup: func(t *testing.T, _ *lifecycleFixture) { t.Setenv("FAKE_TART_FAIL", "clone") }, wantErr: "tart clone failed: fake-tart: clone failed"},
		{name: "set", platform: "tart://img?cpu=2", setup: func(t *testing.T, _ *lifecycleFixture) { t.Setenv("FAKE_TART_FAIL", "set") }, wantErr: "tart set failed"},
		{name: "run exits", setup: func(t *testing.T, _ *lifecycleFixture) {
			t.Setenv("FAKE_TART_RUN_EXIT", "1")
			t.Setenv("FAKE_TART_FAIL", "ip")
		}, wantErr: "`tart run` exited"},
		{name: "ip", setup: func(t *testing.T, _ *lifecycleFixture) { t.Setenv("FAKE_TART_FAIL", "ip") }, wantErr: "tart ip failed"},
		{name: "ssh", setup: func(t *testing.T, f *lifecycleFixture) {
			old := sshReadyWait
			sshReadyWait = time.Second
			t.Cleanup(func() { sshReadyWait = old })
			f.host.SSHPassword = "wrong"
		}, wantErr: "cannot connect to the VM over SSH"},
		{name: "setup", setup: func(_ *testing.T, f *lifecycleFixture) { f.host.LoginShell = "/nonexistent/shell" }, wantErr: "VM setup failed"},
		{name: "platform", setup: func(_ *testing.T, f *lifecycleFixture) {
			f.host.TartBinary = "/nonexistent/tart"
		}, wantErr: "tart was not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			tt.setup(t, f)
			platform := tt.platform
			if platform == "" {
				platform = "tart://img"
			}
			e := f.env(t, platform)
			err := e.Start(false)(context.Background())
			assert.ErrorContains(t, err, tt.wantErr)
			f.assertClean(t)
			// act always calls Close; it must be harmless after a failed start.
			assert.NoError(t, e.Close()(context.Background()))
		})
	}
}

func failedJobContext(err error) context.Context {
	ctx := common.WithJobErrorContainer(context.Background())
	common.SetJobError(ctx, err)
	return ctx
}

func TestLifecycleCloseTearsDownFailedJob(t *testing.T) {
	f := newLifecycleFixture(t)
	e := f.env(t, "tart://img")
	require.NoError(t, e.Start(false)(context.Background()))
	require.NoError(t, e.Close()(failedJobContext(errors.New("step failed"))))
	f.assertClean(t)
}

func TestLifecycleCloseTearsDownCancelledJobEvenWithKeepFailed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.host.KeepFailed = true
	e := f.env(t, "tart://img")
	require.NoError(t, e.Start(false)(context.Background()))
	ctx, cancel := context.WithCancel(failedJobContext(context.Canceled))
	cancel()
	require.NoError(t, e.Close()(ctx))
	f.assertClean(t)
}

func TestLifecycleKeepFailed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.host.KeepFailed = true
	e := f.env(t, "tart://img")
	require.NoError(t, e.Start(false)(context.Background()))
	name := e.VMName()
	v := e.vm
	t.Cleanup(func() {
		_ = v.Stop(context.Background())
		_ = v.Delete(context.Background())
	})

	require.NoError(t, e.Close()(failedJobContext(errors.New("step failed"))))
	assert.Equal(t, []string{name}, f.vms(t), "the VM is kept")
	assert.Len(t, f.lim.slots, 0, "the slot is freed")
	_, err := os.Stat(f.dirs.Root)
	assert.NoError(t, err, "the shared job directory is kept with the VM")
	// A later Remove does not delete the kept VM either.
	require.NoError(t, e.Remove()(context.Background()))
	assert.Equal(t, []string{name}, f.vms(t))
}

func TestLifecycleSucceededJobIgnoresKeepFailed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.host.KeepFailed = true
	e := f.env(t, "tart://img")
	require.NoError(t, e.Start(false)(context.Background()))
	require.NoError(t, e.Close()(common.WithJobErrorContainer(context.Background())))
	f.assertClean(t)
}

func fakeVM(t *testing.T, f *fakeTart, name, state string) {
	t.Helper()
	dir := filepath.Join(f.state, "vms", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state"), []byte(state+"\n"), 0o600))
}

func TestLifecycleLeftoversAndHostLimit(t *testing.T) {
	f := newLifecycleFixture(t)
	fakeVM(t, f.fake, "act-job-aaaaaa", "stopped")
	fakeVM(t, f.fake, "act-job-bbbbbb", "running")
	fakeVM(t, f.fake, "other-vm", "stopped")
	e := f.env(t, "tart://img")
	require.NoError(t, e.Start(false)(context.Background()))
	assert.ElementsMatch(t, []string{"act-job-bbbbbb", "other-vm", e.VMName()}, f.vms(t))
	require.NoError(t, e.Remove()(context.Background()))

	fakeVM(t, f.fake, "someone-else", "running")
	require.NoError(t, os.MkdirAll(f.dirs.Act, 0o755))
	e = f.env(t, "tart://img")
	err := e.Start(false)(context.Background())
	assert.ErrorContains(t, err, "already runs 2 Tart VMs")
	assert.Len(t, f.lim.slots, 0)
}

func TestLifecycleWaitsForSlot(t *testing.T) {
	f := newLifecycleFixture(t)
	f.lim = newLimiter(1)
	held, err := f.lim.Acquire(context.Background())
	require.NoError(t, err)
	e := f.env(t, "tart://img")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = e.Start(false)(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, f.vms(t))
	held.Release()
}

func TestParseSetupOutput(t *testing.T) {
	facts, env, err := parseSetupOutput("noise\narch=arm64\nversion=26.1\nnode=/opt/homebrew/bin/node\n" + envMarker + "A=1\x00B=x=y\x00MULTI=a\nb\x00")
	require.NoError(t, err)
	assert.Equal(t, guestFacts{arch: "arm64", osVersion: "26.1", node: "/opt/homebrew/bin/node"}, facts)
	assert.Equal(t, map[string]string{"A": "1", "B": "x=y", "MULTI": "a\nb"}, env)
	_, _, err = parseSetupOutput("arch=arm64\n")
	assert.Error(t, err)
}
