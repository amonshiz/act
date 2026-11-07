package tart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIRunErrors(t *testing.T) {
	f := newFakeTart(t)
	c := newCLI(f.bin)
	ctx := context.Background()

	t.Setenv("FAKE_TART_FAIL", "clone")
	_, _, err := c.run(ctx, "clone", "img", "vm")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTartFailed))
	assert.Equal(t, "tart clone failed: fake-tart: clone failed", err.Error())

	_, _, err = newCLI(filepath.Join(t.TempDir(), "missing")).run(ctx, "list")
	assert.True(t, errors.Is(err, ErrTartNotFound), "%v", err)
	assert.True(t, errors.Is(newCLI(filepath.Join(t.TempDir(), "missing")).lookPath(), ErrTartNotFound))
	assert.NoError(t, c.lookPath())
}

func TestCLIListVMs(t *testing.T) {
	f := newFakeTart(t)
	c := newCLI(f.bin)
	ctx := context.Background()

	vms, err := c.listVMs(ctx)
	require.NoError(t, err)
	assert.Empty(t, vms)

	require.NoError(t, newVM(c, "a").Clone(ctx, "img"))
	require.NoError(t, os.WriteFile(filepath.Join(f.state, "vms", "a", "state"), []byte("running\n"), 0o600))
	require.NoError(t, newVM(c, "b").Clone(ctx, "img"))
	vms, err = c.listVMs(ctx)
	require.NoError(t, err)
	require.Len(t, vms, 2)
	assert.Equal(t, vmInfo{Name: "a", State: "running", Running: true, Source: "local"}, vms[0])
	assert.True(t, vms[0].isRunning())
	assert.False(t, vms[1].isRunning())
	assert.Equal(t, "list --source local --format json", f.calls(t)[0])
}

func TestVMCommands(t *testing.T) {
	f := newFakeTart(t)
	c := newCLI(f.bin)
	ctx := context.Background()
	v := newVM(c, "act-job-abc123")

	require.NoError(t, c.pull(ctx, "ghcr.io/x/y:latest"))
	require.NoError(t, v.Clone(ctx, "ghcr.io/x/y:latest"))
	require.NoError(t, v.Set(ctx, 0, 0))
	require.NoError(t, v.Set(ctx, 4, 8192))

	actDir, workDir := t.TempDir(), t.TempDir()
	require.NoError(t, v.Run([]sharedDir{{"act", actDir}, {"work", workDir}}, runOptions{Headless: true, SoftnetAllow: "10.0.0.1/32"}, nil))
	ip, err := v.IP(ctx, "dhcp", 120)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ip)

	require.Eventually(t, func() bool {
		_, err := os.Lstat(filepath.Join(f.mount, "work"))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	link, err := os.Readlink(filepath.Join(f.mount, "act"))
	require.NoError(t, err)
	assert.Equal(t, actDir, link)

	start := time.Now()
	require.NoError(t, v.Stop(ctx))
	assert.Less(t, time.Since(start), 5*time.Second)
	require.NoError(t, v.Delete(ctx))
	assert.False(t, f.vmExists("act-job-abc123"))

	assert.Equal(t, []string{
		"pull ghcr.io/x/y:latest",
		"clone ghcr.io/x/y:latest act-job-abc123",
		"set act-job-abc123 --cpu 4 --memory 8192",
		"run --no-graphics --net-softnet-allow=10.0.0.1/32 --dir act:" + actDir + ":tag=act --dir work:" + workDir + ":tag=act act-job-abc123",
		"ip act-job-abc123 --wait 120 --resolver dhcp",
		"delete act-job-abc123",
	}, f.calls(t))
}

func TestVMRunArgs(t *testing.T) {
	args, err := runArgs("vm", nil, runOptions{Softnet: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "--net-softnet", "vm"}, args)

	_, err = runArgs("vm", []sharedDir{{"act", "/a:b"}}, runOptions{})
	assert.ErrorContains(t, err, "contains ':'")
	_, err = runArgs("vm", []sharedDir{{"a/b", "/a"}}, runOptions{})
	assert.Error(t, err)
}

func TestVMIPFailsWhenRunExits(t *testing.T) {
	f := newFakeTart(t)
	c := newCLI(f.bin)
	ctx := context.Background()
	v := newVM(c, "vm")
	require.NoError(t, v.Clone(ctx, "img"))
	t.Setenv("FAKE_TART_RUN_EXIT", "1")
	t.Setenv("FAKE_TART_FAIL", "ip")
	require.NoError(t, v.Run(nil, runOptions{}, nil))
	<-v.exited()
	_, err := v.IP(ctx, "dhcp", 1)
	assert.ErrorContains(t, err, "`tart run` exited")
}

func TestVMStopWithoutRunUsesTartStop(t *testing.T) {
	f := newFakeTart(t)
	v := newVM(newCLI(f.bin), "vm")
	require.NoError(t, v.Stop(context.Background()))
	assert.Equal(t, []string{"stop vm --timeout 10"}, f.calls(t))
}
