//go:build !windows

package tart

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLifecycleAgentTransport(t *testing.T) {
	f := newLifecycleFixture(t)
	e := f.env(t, "tart://img?transport=agent")
	ctx := context.Background()
	require.NoError(t, e.Start(false)(ctx))
	name := e.VMName()
	_, isAgent := e.currentTransport().(*agentTransport)
	assert.True(t, isAgent)

	require.NoError(t, e.Exec([]string{"sh", "-c", `printf '%s|%s' "$A" "$INPUT_NODE-VERSION" > out.txt; env | grep '^INPUT_NODE-VERSION=' >> out.txt`},
		map[string]string{"A": "it's", "INPUT_NODE-VERSION": "20"}, "", "")(ctx))
	b, err := os.ReadFile(filepath.Join(f.dirs.Work, "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "it's|-VERSIONINPUT_NODE-VERSION=20\n", string(b))

	assert.EqualError(t, e.Exec([]string{"sh", "-c", "exit 3"}, nil, "", "")(ctx), "exitcode '3': failure")
	assert.ErrorContains(t, e.Exec([]string{"nonexistent-cmd-for-act"}, nil, "", "")(ctx), "exitcode '127'")

	require.NoError(t, e.Remove()(ctx))
	f.assertClean(t)

	calls := f.fake.calls(t)
	assert.Contains(t, calls, "exec "+name+" true")
	for _, c := range calls {
		assert.False(t, strings.HasPrefix(c, "ip "), "agent transport does not need an IP: %s", c)
		assert.NotContains(t, c, "it's", "values stay off the tart exec command line")
	}
}

func TestLifecycleAgentFallsBackToSSH(t *testing.T) {
	f := newLifecycleFixture(t)
	old := agentReadyWait
	agentReadyWait = 500 * time.Millisecond
	t.Cleanup(func() { agentReadyWait = old })
	t.Setenv("FAKE_TART_FAIL", "exec")
	e := f.env(t, "tart://img?transport=agent")
	ctx := context.Background()
	require.NoError(t, e.Start(false)(ctx))
	_, isSSH := e.currentTransport().(*sshTransport)
	assert.True(t, isSSH)
	require.NoError(t, e.Exec([]string{"true"}, nil, "", "")(ctx))
	require.NoError(t, e.Remove()(ctx))
	f.assertClean(t)
}

func TestAgentCancelKillsProcessTree(t *testing.T) {
	f := newLifecycleFixture(t)
	e := f.env(t, "tart://img?transport=agent")
	require.NoError(t, e.Start(false)(context.Background()))
	t.Cleanup(func() { _ = e.Remove()(context.Background()) })

	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- e.Exec([]string{"sh", "-c", "sleep 30 & echo $! > " + pidFile + "; wait"}, nil, "", "")(ctx)
	}()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		assert.ErrorContains(t, err, "this step has been cancelled")
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after cancel")
	}
	assert.Less(t, time.Since(start), 6*time.Second)
	assert.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 50*time.Millisecond)
}
