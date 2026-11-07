package tart

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/container"
)

// Config is what the runner passes to New.
type Config struct {
	// Platform is the tart:// URI from -P.
	Platform string
	// Name prefixes the VM name; the runner passes its job container name.
	Name string
	// Dirs are the per-job host directories, already created.
	Dirs JobDirs
	// Host holds the flag-level settings.
	Host HostConfig
	// Stdout receives step output.
	Stdout io.Writer
}

// Environment runs a job in a Tart VM. File operations happen on the host
// side of the directories shared into the guest; only commands cross into
// the VM.
type Environment struct {
	cfg   HostConfig
	opts  Options
	dirs  JobDirs
	paths *pathMap

	// files performs every file operation, on host paths. It is a named
	// field rather than an embedded one so none of its methods (notably
	// ExecWithCmdLine) are promoted onto Environment.
	files *container.HostEnvironment

	mu        sync.Mutex
	stdout    io.Writer
	transport transport
	loginEnv  map[string]string
}

// New validates the platform and returns an Environment that is not started.
func New(c Config) (*Environment, error) {
	opts, err := ParsePlatform(c.Platform)
	if err != nil {
		return nil, err
	}
	host := c.Host.withDefaults()
	home := host.GuestRoot
	if home == "" {
		home = path.Join("/Users", host.SSHUser)
	}
	e := &Environment{
		cfg:    host,
		opts:   opts,
		dirs:   c.Dirs,
		paths:  newPathMap(c.Dirs, home),
		stdout: c.Stdout,
	}
	e.files = &container.HostEnvironment{
		Path:      c.Dirs.Work,
		TmpDir:    c.Dirs.Tmp,
		ToolCache: e.paths.toolCache(),
		Workdir:   c.Dirs.Workdir,
		ActPath:   c.Dirs.Act,
		StdOut:    c.Stdout,
	}
	return e, nil
}

// Options returns the parsed platform options.
func (e *Environment) Options() Options { return e.opts }

func (e *Environment) toHost(guestPath string) (string, error) {
	return e.paths.toHost(guestPath)
}

func errorExecutor(err error) common.Executor {
	return func(context.Context) error { return err }
}

func (e *Environment) Copy(destPath string, files ...*container.FileEntry) common.Executor {
	host, err := e.toHost(destPath)
	if err != nil {
		return errorExecutor(err)
	}
	return e.files.Copy(host, files...)
}

func (e *Environment) CopyTarStream(ctx context.Context, destPath string, tarStream io.Reader) error {
	host, err := e.toHost(destPath)
	if err != nil {
		return err
	}
	return e.files.CopyTarStream(ctx, host, tarStream)
}

// CopyDir copies the host directory srcPath to the guest path destPath.
func (e *Environment) CopyDir(destPath string, srcPath string, useGitIgnore bool) common.Executor {
	host, err := e.toHost(destPath)
	if err != nil {
		return errorExecutor(err)
	}
	return e.files.CopyDir(host, srcPath, useGitIgnore)
}

func (e *Environment) GetContainerArchive(ctx context.Context, srcPath string) (io.ReadCloser, error) {
	host, err := e.toHost(srcPath)
	if err != nil {
		return nil, err
	}
	return e.files.GetContainerArchive(ctx, host)
}

func (e *Environment) UpdateFromEnv(srcPath string, env *map[string]string) common.Executor {
	host, err := e.toHost(srcPath)
	if err != nil {
		return errorExecutor(err)
	}
	return e.files.UpdateFromEnv(host, env)
}

// ToContainerPath maps a host path, chiefly the project directory, to the
// guest. Paths outside the shared directories are returned unchanged.
func (e *Environment) ToContainerPath(hostPath string) string {
	p, ok := e.paths.toGuest(hostPath)
	if !ok {
		common.Logger(context.Background()).Debugf("tart: %s is not shared with the VM; using it unchanged", hostPath)
	}
	return p
}

func (e *Environment) GetActPath() string { return e.paths.actPath() }

func (*Environment) GetPathVariableName() string { return "PATH" }

func (*Environment) JoinPathVariable(paths ...string) string { return strings.Join(paths, ":") }

func (*Environment) IsEnvironmentCaseInsensitive() bool { return false }

// DefaultShell is bash, as on GitHub's macOS runners.
func (*Environment) DefaultShell(map[string]string) string { return "bash" }

// --- lifecycle and exec; implemented in later changes ---

var errNotImplemented = errors.New("tart: not implemented yet")

func (e *Environment) Pull(bool) common.Executor                 { return errorExecutor(nil) }
func (e *Environment) Create([]string, []string) common.Executor { return errorExecutor(nil) }
func (e *Environment) Start(bool) common.Executor                { return errorExecutor(errNotImplemented) }
func (e *Environment) ExecWithCmdLine(command []string, _ string, env map[string]string, user, workdir string) common.Executor {
	return e.Exec(command, env, user, workdir)
}
func (e *Environment) UpdateFromImageEnv(*map[string]string) common.Executor {
	return errorExecutor(nil)
}
func (e *Environment) Remove() common.Executor     { return errorExecutor(nil) }
func (e *Environment) Close() common.Executor      { return errorExecutor(nil) }
func (e *Environment) DefaultPathVariable() string { return defaultPath }
func (e *Environment) GetRunnerContext(context.Context) map[string]interface{} {
	return map[string]interface{}{"os": "macOS", "arch": "ARM64", "temp": e.paths.tmpPath(), "tool_cache": e.paths.toolCache()}
}
func (e *Environment) GetHealth(context.Context) container.Health { return container.HealthStarting }
func (e *Environment) ReplaceLogWriter(stdout, _ io.Writer) (io.Writer, io.Writer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	org := e.stdout
	e.stdout = stdout
	return org, org
}

// defaultPath is the PATH used when the guest login environment has none.
const defaultPath = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

var (
	_ container.ExecutionsEnvironment = (*Environment)(nil)
	_ container.CmdLineExecutor       = (*Environment)(nil)
	_ container.DefaultShellProvider  = (*Environment)(nil)
)
