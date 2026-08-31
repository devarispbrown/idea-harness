//go:build unix

package cli

import "syscall"

// sysProcAttrDetach starts the managed daemon in its own session so it survives
// the CLI process exiting.
var sysProcAttrDetach = &syscall.SysProcAttr{Setsid: true}
