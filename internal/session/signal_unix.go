//go:build !windows

package session

import (
	"os"
	"syscall"
)

func stopSignal() os.Signal { return syscall.SIGTERM }
