//go:build !windows

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nektos/act/internal/sshtest"
	"github.com/nektos/act/pkg/tart"
)

// TestRunEventTart runs workflows on a tart:// platform against a fake tart
// CLI and an in-process SSH server that runs commands locally, so guest
// paths are real local paths. It needs neither Docker nor a Mac.
func TestRunEventTart(t *testing.T) {
	shardSkip(t)

	fakeTart, err := filepath.Abs("../tart/testdata/fake-tart")
	require.NoError(t, err)
	home := t.TempDir()
	state := t.TempDir()
	t.Setenv("FAKE_TART_STATE", state)
	t.Setenv("FAKE_TART_MOUNT", filepath.Join(home, "act"))
	t.Setenv("ACT_TART_HOST_ONLY", "must-not-leak")

	srv := sshtest.New(t, "admin", "secret")
	// Like sshd, give sessions a minimal environment rather than the test's.
	srv.SetEnv([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USER=admin", "LANG=C.UTF-8"})

	cfg := &Config{
		Tart: tart.HostConfig{
			SSHUser:           "admin",
			SSHPassword:       "secret",
			TartBinary:        fakeTart,
			SSHPort:           srv.Port(),
			GuestRoot:         home,
			LoginShell:        "/bin/sh",
			SkipPlatformCheck: true,
			SkipMount:         true,
		},
	}
	platforms := map[string]string{"macos-26": "tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest"}

	tables := []TestJobFileInfo{
		{workdir, "tart-basic", "push", "", platforms, secrets},
		{workdir, "tart-env-files", "push", "", platforms, secrets},
		{workdir, "tart-refuse-docker", "push", "Job 'docker-url' failed", platforms, secrets},
	}
	if _, err := exec.LookPath("node"); err == nil {
		tables = append(tables, TestJobFileInfo{workdir, "tart-node-action", "push", "", platforms, secrets})
	} else {
		t.Log("node is not installed; skipping tart-node-action")
	}

	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			table.runTest(context.Background(), t, cfg)
		})
	}

	vms, err := os.ReadDir(filepath.Join(state, "vms"))
	if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	assert.Empty(t, vms, "every VM is deleted")
}
