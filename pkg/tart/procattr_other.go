//go:build windows || plan9

package tart

import "syscall"

func newSessionAttr() *syscall.SysProcAttr {
	return nil
}
