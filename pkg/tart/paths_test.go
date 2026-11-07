package tart

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nektos/act/pkg/container"
)

func testDirs(bind bool) JobDirs {
	return JobDirs{
		Root:    "/cache/act/0123",
		Act:     "/cache/act/0123/act",
		Work:    "/cache/act/0123/hostexecutor",
		Tmp:     "/cache/act/0123/tmp",
		Workdir: "/src/my project",
		Bind:    bind,
	}
}

func TestPathMapToGuest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host paths in this table are POSIX paths")
	}
	tests := []struct {
		bind bool
		host string
		want string
		ok   bool
	}{
		{false, "/src/my project", "/Users/admin/act/work", true},
		{false, "/src/my project/", "/Users/admin/act/work", true},
		{false, "/src/my project/sub/dir", "/Users/admin/act/work/sub/dir", true},
		{true, "/src/my project", "/Users/admin/act/bind", true},
		{true, "/src/my project/sub", "/Users/admin/act/bind/sub", true},
		{false, "/cache/act/0123/act", "/Users/admin/act/act", true},
		{false, "/cache/act/0123/act/workflow/event.json", "/Users/admin/act/act/workflow/event.json", true},
		{false, "/cache/act/0123/tmp/x", "/Users/admin/act/tmp/x", true},
		{false, "/cache/act/0123/hostexecutor/a", "/Users/admin/act/work/a", true},
		{false, "/cache/act/0123/act/../../../etc/passwd", "/cache/act/0123/act/../../../etc/passwd", false},
		{false, "/src/my projectX", "/src/my projectX", false},
		{false, "/etc/passwd", "/etc/passwd", false},
	}
	for _, tt := range tests {
		m := newPathMap(testDirs(tt.bind), "/Users/admin")
		got, ok := m.toGuest(tt.host)
		assert.Equal(t, tt.want, got, "%s bind=%t", tt.host, tt.bind)
		assert.Equal(t, tt.ok, ok, "%s bind=%t", tt.host, tt.bind)
	}
}

func TestPathMapToHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host paths in this table are POSIX paths")
	}
	tests := []struct {
		bind    bool
		guest   string
		want    string
		wantErr bool
	}{
		{false, "/Users/admin/act/act", "/cache/act/0123/act", false},
		{false, "/Users/admin/act/act/", "/cache/act/0123/act/", false},
		{false, "/Users/admin/act/act/workflow/event.json", "/cache/act/0123/act/workflow/event.json", false},
		{false, "/Users/admin/act/work/a b/c", "/cache/act/0123/hostexecutor/a b/c", false},
		{false, "/Users/admin/act/tmp", "/cache/act/0123/tmp", false},
		{false, "/Users/admin/act/./act//x", "/cache/act/0123/act/x", false},
		{true, "/Users/admin/act/bind/f", "/src/my project/f", false},
		{false, "/Users/admin/act/bind/f", "", true},
		{false, "/Users/admin/act/act/../../.ssh/id_rsa", "", true},
		{false, "/Users/admin/act/actx", "", true},
		{false, "/Users/admin/act", "", true},
		{false, "/Users/admin/hostedtoolcache/node", "", true},
		{false, "act/workflow", "", true},
		{false, "", "", true},
	}
	for _, tt := range tests {
		m := newPathMap(testDirs(tt.bind), "/Users/admin")
		got, err := m.toHost(tt.guest)
		if tt.wantErr {
			assert.Error(t, err, tt.guest)
			continue
		}
		assert.NoError(t, err, tt.guest)
		assert.Equal(t, tt.want, got, tt.guest)
	}
}

func TestPathMapRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host paths in this table are POSIX paths")
	}
	for _, bind := range []bool{false, true} {
		m := newPathMap(testDirs(bind), "/Users/admin/")
		for _, p := range []string{
			"/cache/act/0123/act",
			"/cache/act/0123/act/workflow/1.sh",
			"/cache/act/0123/hostexecutor/x/y",
			"/cache/act/0123/tmp/z",
		} {
			g, ok := m.toGuest(p)
			require.True(t, ok, p)
			h, err := m.toHost(g)
			require.NoError(t, err, g)
			assert.Equal(t, p, h)
		}
	}
	m := newPathMap(testDirs(false), "/Users/admin")
	assert.Equal(t, "/Users/admin/act/act", m.actPath())
	assert.Equal(t, "/Users/admin/act/tmp", m.tmpPath())
	assert.Equal(t, "/Users/admin/hostedtoolcache", m.toolCache())
	assert.Len(t, m.shares, 3)
	assert.Len(t, newPathMap(testDirs(true), "/Users/admin").shares, 4)
}

func newTestEnvironment(t *testing.T, bind bool) (*Environment, JobDirs) {
	t.Helper()
	root := t.TempDir()
	dirs := JobDirs{
		Root:    root,
		Act:     filepath.Join(root, "act"),
		Work:    filepath.Join(root, "hostexecutor"),
		Tmp:     filepath.Join(root, "tmp"),
		Workdir: t.TempDir(),
		Bind:    bind,
	}
	for _, d := range []string{dirs.Act, dirs.Work, dirs.Tmp} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	e, err := New(Config{Platform: "tart://img", Name: "act-test", Dirs: dirs, Host: HostConfig{GuestRoot: "/Users/runner"}, Stdout: io.Discard})
	require.NoError(t, err)
	return e, dirs
}

func TestEnvironmentFileOps(t *testing.T) {
	e, dirs := newTestEnvironment(t, false)
	ctx := context.Background()

	assert.Equal(t, "/Users/runner/act/act", e.GetActPath())
	assert.Equal(t, "/Users/runner/act/work", e.ToContainerPath(dirs.Workdir))
	assert.Equal(t, "PATH", e.GetPathVariableName())
	assert.Equal(t, "a:b", e.JoinPathVariable("a", "b"))
	assert.False(t, e.IsEnvironmentCaseInsensitive())
	assert.Equal(t, "bash", e.DefaultShell(nil))

	require.NoError(t, e.Copy(e.GetActPath()+"/", &container.FileEntry{Name: "workflow/event.json", Mode: 0o644, Body: "{}"})(ctx))
	b, err := os.ReadFile(filepath.Join(dirs.Act, "workflow", "event.json"))
	require.NoError(t, err)
	assert.Equal(t, "{}", string(b))

	rc, err := e.GetContainerArchive(ctx, e.GetActPath()+"/workflow/event.json")
	require.NoError(t, err)
	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	require.NoError(t, err)
	assert.Equal(t, "event.json", hdr.Name)
	body, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(body))

	require.NoError(t, e.Copy(e.GetActPath(), &container.FileEntry{Name: "workflow/envs.txt", Mode: 0o644, Body: "A=1\nB<<EOF\nx\nEOF\n"})(ctx))
	env := map[string]string{}
	require.NoError(t, e.UpdateFromEnv(e.GetActPath()+"/workflow/envs.txt", &env)(ctx))
	assert.Equal(t, map[string]string{"A": "1", "B": "x"}, env)

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "action.yml"), []byte("name: x"), 0o600))
	require.NoError(t, e.CopyDir(e.GetActPath()+"/actions/x/", src+"/", false)(ctx))
	_, err = os.Stat(filepath.Join(dirs.Act, "actions", "x", "action.yml"))
	assert.NoError(t, err)

	// Paths outside the shared directories never reach the host.
	assert.Error(t, e.Copy("/etc", &container.FileEntry{Name: "x", Body: "x"})(ctx))
	_, err = e.GetContainerArchive(ctx, "/Users/runner/act/act/../../.ssh/id_rsa")
	assert.Error(t, err)
	assert.Error(t, e.CopyTarStream(ctx, "/tmp/x", nil))
	assert.Error(t, e.UpdateFromEnv("/etc/passwd", &env)(ctx))
	assert.Error(t, e.CopyDir("relative", src, false)(ctx))
}

func TestEnvironmentBindWorkspace(t *testing.T) {
	e, dirs := newTestEnvironment(t, true)
	assert.Equal(t, "/Users/runner/act/bind", e.ToContainerPath(dirs.Workdir))
	require.NoError(t, e.Copy("/Users/runner/act/bind/out", &container.FileEntry{Name: "f", Mode: 0o644, Body: "y"})(context.Background()))
	_, err := os.Stat(filepath.Join(dirs.Workdir, "out", "f"))
	assert.NoError(t, err)
}

func TestEnvironmentDoesNotPromoteHostExec(t *testing.T) {
	var e interface{} = &Environment{}
	_, isHost := e.(*container.HostEnvironment)
	assert.False(t, isHost)
}
