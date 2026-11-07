package tart

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePlatform(t *testing.T) {
	def := func(image string) Options {
		return Options{Image: image, Headless: true, Resolver: "dhcp", Transport: TransportSSH}
	}
	tests := []struct {
		uri     string
		want    Options
		wantErr string
	}{
		{uri: "tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest", want: def("ghcr.io/cirruslabs/macos-tahoe-xcode:latest")},
		{uri: "TART://my-base-vm", want: def("my-base-vm")},
		{uri: "tart://img?cpu=4&memory=8192", want: func() Options { o := def("img"); o.CPU = 4; o.MemoryMB = 8192; return o }()},
		{uri: "tart://img?softnet=1", want: func() Options { o := def("img"); o.Softnet = true; return o }()},
		{uri: "tart://img?softnet_allow=192.168.1.10/32,10.0.0.0/8", want: func() Options {
			o := def("img")
			o.Softnet = true
			o.SoftnetAllow = "192.168.1.10/32,10.0.0.0/8"
			return o
		}()},
		{uri: "tart://img?headless=0&pull=1&resolver=agent&transport=agent", want: func() Options {
			o := def("img")
			o.Headless = false
			o.Pull = true
			o.Resolver = "agent"
			o.Transport = TransportAgent
			return o
		}()},
		{uri: "tart://img?cpus=4", wantErr: `unknown tart option "cpus"`},
		{uri: "tart://img?cpu=0", wantErr: "positive integer"},
		{uri: "tart://img?cpu=x", wantErr: "positive integer"},
		{uri: "tart://img?cpu=1&cpu=2", wantErr: "more than once"},
		{uri: "tart://img?softnet=maybe", wantErr: "expected 0 or 1"},
		{uri: "tart://img?softnet_allow=10.0.0.1", wantErr: "not a CIDR"},
		{uri: "tart://img?resolver=mdns", wantErr: "expected dhcp, arp or agent"},
		{uri: "tart://img?transport=vnc", wantErr: "expected ssh or agent"},
		{uri: "tart://", wantErr: "names no image"},
		{uri: "tart://?cpu=1", wantErr: "names no image"},
		{uri: "docker://img", wantErr: "does not start with tart://"},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			got, err := ParsePlatform(tt.uri)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "tart://img", Redact("tart://img?softnet_allow=10.0.0.1/32"))
	o, err := ParsePlatform("tart://img?cpu=2")
	assert.NoError(t, err)
	assert.Equal(t, "tart://img", o.Redacted())
}

func TestHostConfigHidesPassword(t *testing.T) {
	h := HostConfig{SSHUser: "admin", SSHPassword: "hunter2"}
	for _, s := range []string{h.String(), fmt.Sprintf("%v", h), fmt.Sprintf("%+v", h), fmt.Sprintf("%#v", h)} {
		assert.NotContains(t, s, "hunter2")
	}
	d := HostConfig{}.withDefaults()
	assert.Equal(t, "admin", d.SSHUser)
	assert.Equal(t, "admin", d.SSHPassword)
	assert.Equal(t, 2, d.MaxVMs)
	assert.Equal(t, 22, d.SSHPort)
	assert.Equal(t, "/bin/zsh", d.LoginShell)
	assert.Empty(t, HostConfig{SSHKeyPath: "/k"}.withDefaults().SSHPassword)
}
