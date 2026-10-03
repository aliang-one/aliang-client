package services

import (
	"fmt"

	"aliang.one/nursorgate/common/logger"
)

// safeGo runs fn on its own goroutine with a recover guard: a panicking task
// (e.g. terminal.create driving PTY spawn + scrollback replay) must not kill
// the whole agent process — there is no supervisor here, only the reconnect
// loop, and a dead process takes every live PTY with it. Logging is
// content-free (task name + panic value only), [AGENT-BOOT] key=value style.
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error(fmt.Sprintf("[AGENT-BOOT] safe_go panic_recovered task=%s panic=%v", name, r))
			}
		}()
		fn()
	}()
}
