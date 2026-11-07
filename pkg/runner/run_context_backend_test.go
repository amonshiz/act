package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nektos/act/pkg/model"
)

func TestRunContextBackend(t *testing.T) {
	tests := []struct {
		name     string
		job      string
		platform string
		want     jobBackend
		wantErr  string
	}{
		{name: "docker image", job: "runs-on: ubuntu-latest", platform: "node:16", want: backendDocker},
		{name: "unmapped label", job: "runs-on: windows-latest", platform: "node:16", want: backendDocker},
		{name: "self-hosted", job: "runs-on: ubuntu-latest", platform: "-self-hosted", want: backendHost},
		{name: "self-hosted mixed case", job: "runs-on: ubuntu-latest", platform: "-Self-Hosted", want: backendHost},
		{name: "container wins over self-hosted", job: "runs-on: ubuntu-latest\ncontainer: alpine", platform: "-self-hosted", want: backendDocker},
		{name: "container on docker", job: "runs-on: ubuntu-latest\ncontainer: alpine", platform: "node:16", want: backendDocker},
		{name: "tart", job: "runs-on: ubuntu-latest", platform: "tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest", want: backendTart},
		{name: "tart mixed case scheme", job: "runs-on: ubuntu-latest", platform: "TART://my-vm", want: backendTart},
		{name: "tart with query", job: "runs-on: ubuntu-latest", platform: "tart://my-vm?cpu=4", want: backendTart},
		{name: "tart refuses container", job: "runs-on: ubuntu-latest\ncontainer: alpine", platform: "tart://my-vm", want: backendTart, wantErr: "container jobs are only supported on Linux runners"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := createIfTestRunContext(map[string]*model.Job{"job1": createJob(t, tt.job, "")})
			rc.Config.Platforms = map[string]string{"ubuntu-latest": tt.platform}
			got, err := rc.backend(context.Background())
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want == backendHost && tt.wantErr == "", rc.IsHostEnv(context.Background()))
		})
	}
}
