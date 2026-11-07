package container

import (
	"context"

	"github.com/nektos/act/pkg/common"
)

type ExecutionsEnvironment interface {
	Container
	ToContainerPath(string) string
	GetActPath() string
	GetPathVariableName() string
	DefaultPathVariable() string
	JoinPathVariable(...string) string
	GetRunnerContext(ctx context.Context) map[string]interface{}
	// On windows PATH and Path are the same key
	IsEnvironmentCaseInsensitive() bool
}

// CmdLineExecutor is implemented by environments that can use the original,
// unsplit command line of a step (for example to pass it verbatim to
// cmd.exe on Windows).
type CmdLineExecutor interface {
	ExecWithCmdLine(command []string, cmdline string, env map[string]string, user, workdir string) common.Executor
}

// DefaultShellProvider is implemented by environments that pick the default
// shell for run steps themselves. env is the step environment with the job's
// extra PATH entries already applied.
type DefaultShellProvider interface {
	DefaultShell(env map[string]string) string
}
