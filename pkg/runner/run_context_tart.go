package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/container"
	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/tart"
)

var (
	errTartServices         = errors.New("service containers are only supported on Linux runners")
	errTartContainerActions = errors.New("container actions are only supported on Linux runners")
)

// runsInTart reports whether the job container is a Tart VM. Composite
// actions share their parent's job container, so this holds for them too.
func (rc *RunContext) runsInTart() bool {
	_, ok := rc.JobContainer.(*tart.Environment)
	return ok
}

// checkTartJob refuses Docker-only features before a VM is started.
func (rc *RunContext) checkTartJob() error {
	job := rc.Run.Job()
	if len(job.Services) > 0 {
		return errTartServices
	}
	for _, s := range job.Steps {
		if s != nil && s.Type() == model.StepTypeUsesDockerURL {
			return fmt.Errorf("step %q: %w", s.String(), errTartContainerActions)
		}
	}
	return nil
}

func (rc *RunContext) startTartEnvironment() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		if err := rc.checkTartJob(); err != nil {
			return err
		}
		platform := rc.runsOnImage(ctx)
		dirs, err := rc.newJobDirs()
		if err != nil {
			return err
		}
		env, err := tart.New(tart.Config{
			Platform: platform,
			Name:     rc.jobContainerName(),
			Dirs: tart.JobDirs{
				Root:    dirs.root,
				Act:     dirs.act,
				Work:    dirs.work,
				Tmp:     dirs.tmp,
				Workdir: rc.Config.Workdir,
				Bind:    rc.Config.BindWorkdir,
			},
			Host:   rc.Config.Tart,
			Stdout: rc.newStepLogWriter(ctx),
			RunLog: common.NewLineWriter(func(s string) bool {
				logger.Debugf("tart run: %s", strings.TrimRight(s, "\n"))
				return true
			}),
		})
		if err != nil {
			_ = os.RemoveAll(dirs.root)
			return err
		}
		logger.Infof("\U0001f680  Start image=%s", tart.Redact(platform))
		rc.JobContainer = env
		rc.cleanUpJobContainer = env.Remove()
		if err := env.Start(false)(ctx); err != nil {
			return err
		}
		for k, v := range env.GetRunnerContext(ctx) {
			if v, ok := v.(string); ok {
				rc.Env[fmt.Sprintf("RUNNER_%s", strings.ToUpper(k))] = v
			}
		}
		return env.Copy(env.GetActPath()+"/", &container.FileEntry{
			Name: "workflow/event.json",
			Mode: 0o644,
			Body: rc.EventJSON,
		}, &container.FileEntry{
			Name: "workflow/envs.txt",
			Mode: 0o666,
			Body: "",
		})(ctx)
	}
}
