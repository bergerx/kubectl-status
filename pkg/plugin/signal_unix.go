//go:build !windows

package plugin

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func signalName(signal int64) string {
	return unix.SignalName(syscall.Signal(signal))
}
