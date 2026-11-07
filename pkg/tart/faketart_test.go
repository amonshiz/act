package tart

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeTart points tests at testdata/fake-tart.
type fakeTart struct {
	bin   string
	state string
	mount string
}

func newFakeTart(t *testing.T) *fakeTart {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tart is a shell script")
	}
	bin, err := filepath.Abs("testdata/fake-tart")
	require.NoError(t, err)
	f := &fakeTart{bin: bin, state: t.TempDir(), mount: t.TempDir()}
	t.Setenv("FAKE_TART_STATE", f.state)
	t.Setenv("FAKE_TART_MOUNT", f.mount)
	t.Setenv("FAKE_TART_FAIL", "")
	t.Setenv("FAKE_TART_RUN_EXIT", "")
	t.Setenv("FAKE_TART_IP", "")
	return f
}

// calls returns each logged tart invocation.
func (f *fakeTart) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.state, "argv.log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func (f *fakeTart) vmExists(name string) bool {
	_, err := os.Stat(filepath.Join(f.state, "vms", name))
	return err == nil
}
