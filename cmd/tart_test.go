package cmd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewPlatformsSplitsOnFirstEquals(t *testing.T) {
	input := &Input{platforms: []string{
		"macos-15=tart://img?cpu=4&memory=8192",
		"MacOS-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest",
		"ubuntu-latest=node:20",
		"no-equals",
		"=empty-label",
	}}
	p := input.newPlatforms()
	assert.Equal(t, "tart://img?cpu=4&memory=8192", p["macos-15"])
	assert.Equal(t, "tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest", p["macos-26"])
	assert.Equal(t, "node:20", p["ubuntu-latest"])
	assert.NotContains(t, p, "no-equals")
	assert.NotContains(t, p, "")
}

// A project .actrc may pick a tart:// image and its options, which only
// affect a throwaway VM, but not the --tart-* flags, which carry
// credentials and host-wide limits.
func TestProjectActrcTart(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project-actrc")
	writeFile(t, project, `-P macos-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest?cpu=4&softnet=1
--tart-ssh-password hunter2
--tart-ssh-user root
--tart-ssh-key /Users/me/.ssh/id_ed25519
--tart-max-vms 10
--tart-keep-failed
`)
	got := actrcArgs(testFlagSets(t), nil, project, nil)
	assert.Equal(t, []string{"-P", "macos-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest?cpu=4&softnet=1"}, got)
}

func TestTartHostConfig(t *testing.T) {
	t.Setenv(tartSSHPasswordEnv, "from-env")
	i := &Input{tartSSHUser: "admin", tartMaxVMs: 2}
	assert.Equal(t, "from-env", i.tartHostConfig().SSHPassword)
	i.tartSSHPassword = "from-flag"
	assert.Equal(t, "from-flag", i.tartHostConfig().SSHPassword)
	assert.Equal(t, 2, i.tartHostConfig().MaxVMs)
}
