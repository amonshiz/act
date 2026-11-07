package tart

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Names of the per-job shared directories, as they appear in the guest
// under the mount point.
const (
	shareAct  = "act"
	shareWork = "work"
	shareTmp  = "tmp"
	shareBind = "bind"
)

// JobDirs are the per-job host directories, laid out like the ones
// startHostEnvironment creates.
type JobDirs struct {
	Root    string // <ActionCacheDir>/<random>
	Act     string // Root/act
	Work    string // Root/hostexecutor
	Tmp     string // Root/tmp
	Workdir string // the project directory (rc.Config.Workdir)
	Bind    bool   // --bind: share Workdir instead of using Work
}

type root struct {
	guest string // slash-separated guest path
	host  string // host path
}

// pathMap translates between guest and host paths for the shared
// directories. Guest paths always use '/'.
type pathMap struct {
	home      string // guest home directory
	mount     string // guest mount point for the act tag
	workspace string // guest GITHUB_WORKSPACE
	workdir   string // host project directory
	roots     []root
	shares    []sharedDir
}

func newPathMap(dirs JobDirs, guestHome string) *pathMap {
	home := path.Clean(guestHome)
	mount := path.Join(home, "act")
	m := &pathMap{
		home:    home,
		mount:   mount,
		workdir: filepath.Clean(dirs.Workdir),
		shares: []sharedDir{
			{Name: shareAct, Path: dirs.Act},
			{Name: shareWork, Path: dirs.Work},
			{Name: shareTmp, Path: dirs.Tmp},
		},
	}
	m.workspace = path.Join(mount, shareWork)
	if dirs.Bind {
		m.workspace = path.Join(mount, shareBind)
		m.shares = append(m.shares, sharedDir{Name: shareBind, Path: dirs.Workdir})
	}
	for _, s := range m.shares {
		m.roots = append(m.roots, root{guest: path.Join(mount, s.Name), host: filepath.Clean(s.Path)})
	}
	return m
}

func (m *pathMap) actPath() string   { return path.Join(m.mount, shareAct) }
func (m *pathMap) tmpPath() string   { return path.Join(m.mount, shareTmp) }
func (m *pathMap) toolCache() string { return path.Join(m.home, "hostedtoolcache") }

// hostRel reports p relative to base when p is base or inside it.
func hostRel(base, p string) (string, bool) {
	rel, err := filepath.Rel(base, p)
	if err != nil || !filepath.IsLocal(rel) && rel != "." {
		return "", false
	}
	return rel, true
}

// toGuest maps a host path to the guest. The project directory maps to the
// guest workspace, a path inside a shared directory to its guest
// equivalent; anything else is returned unchanged with ok=false.
func (m *pathMap) toGuest(hostPath string) (string, bool) {
	p := filepath.Clean(hostPath)
	best, bestLen := "", -1
	try := func(hostBase, guestBase string) {
		if rel, ok := hostRel(hostBase, p); ok && len(hostBase) > bestLen {
			best, bestLen = path.Join(guestBase, filepath.ToSlash(rel)), len(hostBase)
		}
	}
	try(m.workdir, m.workspace)
	for _, r := range m.roots {
		try(r.host, r.guest)
	}
	if bestLen < 0 {
		return hostPath, false
	}
	return best, true
}

// toHost maps a guest path inside a shared directory to the host. Any other
// path is an error, so a bug cannot reach arbitrary host files.
func (m *pathMap) toHost(guestPath string) (string, error) {
	if !strings.HasPrefix(guestPath, "/") {
		return "", fmt.Errorf("guest path %q is not absolute", guestPath)
	}
	p := path.Clean(guestPath)
	for _, r := range m.roots {
		if p == r.guest {
			return withTrailingSlash(r.host, guestPath), nil
		}
		if rel, ok := strings.CutPrefix(p, r.guest+"/"); ok {
			return withTrailingSlash(filepath.Join(r.host, filepath.FromSlash(rel)), guestPath), nil
		}
	}
	return "", fmt.Errorf("guest path %q is outside the directories shared with the VM", guestPath)
}

func withTrailingSlash(hostPath, guestPath string) string {
	if strings.HasSuffix(guestPath, "/") && !strings.HasSuffix(hostPath, string(filepath.Separator)) {
		return hostPath + string(filepath.Separator)
	}
	return hostPath
}
