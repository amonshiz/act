package tart

import (
	"context"
	"io"
	"sync"
)

// transport runs a command in the guest. SSH and `tart exec` implement it.
type transport interface {
	// Run runs argv in the guest, writing stdout and stderr to out, and
	// returns the command's exit code. A non-nil error means the command's
	// result is unknown (connection lost, killed by a signal, cancelled).
	Run(ctx context.Context, argv []string, out io.Writer) (exitCode int, err error)
	Close() error
}

// lockedWriter serialises writes from the stdout and stderr copiers, since
// act's line writer is not safe for concurrent use.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return len(p), nil
	}
	return l.w.Write(p)
}
