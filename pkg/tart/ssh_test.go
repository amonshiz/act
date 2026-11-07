//go:build !windows

package tart

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nektos/act/internal/sshtest"
)

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// linkShares makes the guest view of the shared directories real local
// paths, as virtiofs does in a VM.
func linkShares(t *testing.T, e *Environment) {
	t.Helper()
	require.NoError(t, os.MkdirAll(e.paths.mount, 0o755))
	for _, s := range e.paths.shares {
		require.NoError(t, os.Symlink(s.Path, filepath.Join(e.paths.mount, s.Name)))
	}
}

// newSSHEnvironment returns an Environment connected to an in-process SSH
// server, without a VM.
func newSSHEnvironment(t *testing.T) (*Environment, *sshtest.Server, *syncBuffer) {
	t.Helper()
	srv := sshtest.New(t, "admin", "secret")
	e, _ := newTestEnvironment(t, false)
	e.paths = newPathMap(e.dirs, t.TempDir())
	linkShares(t, e)
	out := &syncBuffer{}
	e.stdout = out
	cfg, err := sshClientConfig(HostConfig{SSHUser: "admin", SSHPassword: "secret"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tr, err := newSSHTransport(ctx, srv.Addr(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { tr.Close() })
	e.transport = tr
	e.loginEnv = map[string]string{"PATH": os.Getenv("PATH"), "HOME": "/home/guest", "SSH_TTY": "x", "SHLVL": "3", "PWD": "/nope"}
	return e, srv, out
}

func TestExecExitCodes(t *testing.T) {
	e, _, out := newSSHEnvironment(t)
	ctx := context.Background()

	require.NoError(t, e.Exec([]string{"sh", "-c", "echo hello"}, nil, "", "")(ctx))
	assert.Equal(t, "hello\n", out.String())

	assert.EqualError(t, e.Exec([]string{"sh", "-c", "exit 3"}, nil, "", "")(ctx), "exitcode '3': failure")
	assert.ErrorContains(t, e.Exec([]string{"nonexistent-cmd-for-act"}, nil, "", "")(ctx), "exitcode '127': command not found")
	assert.ErrorContains(t, e.Exec([]string{"sh", "-c", "kill -TERM $$"}, nil, "", "")(ctx), "signal SIGTERM")

	entries, err := os.ReadDir(filepath.Join(e.dirs.Act, runDir))
	require.NoError(t, err)
	assert.Empty(t, entries, "launchers and pgid files are removed")
}

func TestExecEnvironment(t *testing.T) {
	e, srv, out := newSSHEnvironment(t)
	env := map[string]string{
		"QUOTES":             `it's "quoted"`,
		"NEWLINE":            "a\nb",
		"DOLLAR":             "$HOME `id` $(id)",
		"INPUT_NODE-VERSION": "20.x",
		"UID":                "readonly-in-bash",
		"HOME":               "/override",
	}
	script := `printf '%s|' "$QUOTES" "$NEWLINE" "$DOLLAR" "$HOME" "${SSH_TTY-unset}" "${SHLVL-unset}" "$(pwd)"; env | grep '^INPUT_NODE-VERSION='`
	require.NoError(t, e.Exec([]string{"sh", "-c", script}, env, "", "")(context.Background()))
	assert.Equal(t, `it's "quoted"|a`+"\n"+`b|$HOME `+"`id`"+` $(id)|/override|unset|`, out.String()[:len(`it's "quoted"|a`+"\n"+`b|$HOME `+"`id`"+` $(id)|/override|unset|`)])
	assert.Contains(t, out.String(), "INPUT_NODE-VERSION=20.x\n")
	assert.Contains(t, out.String(), "|"+e.paths.workspace+"|")

	for _, c := range srv.Commands() {
		assert.NotContains(t, c, "quoted", "values stay off the command line")
		assert.NotContains(t, c, "20.x")
	}

	assert.ErrorContains(t, e.Exec([]string{"true"}, map[string]string{"A=B": "x"}, "", "")(context.Background()), "invalid environment variable name")
}

func TestExecWorkdirAndStdin(t *testing.T) {
	e, _, out := newSSHEnvironment(t)
	ctx := context.Background()
	require.NoError(t, os.MkdirAll(filepath.Join(e.dirs.Work, "sub"), 0o755))

	require.NoError(t, e.Exec([]string{"sh", "-c", "pwd"}, nil, "", "sub")(ctx))
	require.NoError(t, e.Exec([]string{"sh", "-c", "pwd"}, nil, "", e.paths.tmpPath())(ctx))
	require.NoError(t, e.Exec([]string{"sh", "-c", "read x || echo eof"}, nil, "", "")(ctx))
	assert.Equal(t, e.paths.workspace+"/sub\n"+e.paths.tmpPath()+"\neof\n", out.String())

	assert.Error(t, e.Exec([]string{"true"}, nil, "", "missing")(ctx))
}

func TestExecInterleavedOutput(t *testing.T) {
	e, _, out := newSSHEnvironment(t)
	script := `i=0; while [ $i -lt 200 ]; do echo "out $i"; echo "err $i" >&2; i=$((i+1)); done`
	require.NoError(t, e.Exec([]string{"sh", "-c", script}, nil, "", "")(context.Background()))
	s := out.String()
	for i := 0; i < 200; i++ {
		assert.Contains(t, s, fmt.Sprintf("out %d\n", i))
		assert.Contains(t, s, fmt.Sprintf("err %d\n", i))
	}
}

func TestExecCancelKillsProcessTree(t *testing.T) {
	e, _, _ := newSSHEnvironment(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- e.Exec([]string{"sh", "-c", "sleep 30 & echo $! > " + pidFile + "; wait"}, nil, "", "")(ctx)
	}()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil || len(bytes.TrimSpace(b)) == 0 {
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
	assert.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) != nil
	}, 2*time.Second, 50*time.Millisecond, "background sleep %d survived", pid)
}

func TestExecRedialsBeforeSessionOnly(t *testing.T) {
	e, srv, out := newSSHEnvironment(t)
	ctx := context.Background()

	require.NoError(t, e.Exec([]string{"sh", "-c", "echo one"}, nil, "", "")(ctx))
	srv.DropConnections()
	require.NoError(t, e.Exec([]string{"sh", "-c", "echo two"}, nil, "", "")(ctx))
	assert.Equal(t, "one\ntwo\n", out.String())
	assert.Len(t, srv.Commands(), 2)

	// A drop while a command runs is reported, not retried.
	errc := make(chan error, 1)
	go func() { errc <- e.Exec([]string{"sh", "-c", "sleep 2"}, nil, "", "")(ctx) }()
	require.Eventually(t, func() bool { return len(srv.Commands()) == 3 }, 5*time.Second, 10*time.Millisecond)
	srv.DropConnections()
	select {
	case err := <-errc:
		assert.ErrorIs(t, err, errConnectionLost)
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after the connection dropped")
	}
	assert.Len(t, srv.Commands(), 3)
}

func TestExecWithoutTransport(t *testing.T) {
	e, _ := newTestEnvironment(t, false)
	assert.ErrorIs(t, e.Exec([]string{"true"}, nil, "", "")(context.Background()), errNotStarted)
	assert.ErrorIs(t, e.ExecWithCmdLine([]string{"true"}, "true", nil, "", "")(context.Background()), errNotStarted)
}

func TestSSHClientConfigKey(t *testing.T) {
	_, err := sshClientConfig(HostConfig{SSHUser: "a", SSHKeyPath: filepath.Join(t.TempDir(), "missing")})
	assert.ErrorContains(t, err, "cannot read --tart-ssh-key")
	bad := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(bad, []byte("not a key"), 0o600))
	_, err = sshClientConfig(HostConfig{SSHUser: "a", SSHKeyPath: bad})
	assert.ErrorContains(t, err, "cannot parse --tart-ssh-key")
}
