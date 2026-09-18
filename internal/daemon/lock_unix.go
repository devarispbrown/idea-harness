//go:build unix

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockStateDir takes an exclusive, non-blocking lock on the state directory,
// held for the life of the process and released by the kernel if it dies.
func lockStateDir(stateRoot string) (func(), error) {
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateRoot, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("daemon: another daemon is already using %s (on a different address); stop it with `idea daemon stop` first", stateRoot)
	}
	return func() { f.Close() }, nil
}
