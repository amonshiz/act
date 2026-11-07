//go:build !windows && !plan9

package tart

import "syscall"

// newSessionAttr starts a child in its own session, so a Ctrl-C in act's
// terminal does not reach tart directly and act controls the shutdown.
func newSessionAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
