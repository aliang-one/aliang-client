//go:build windows

package services

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

// startAgentPTY starts the shell attached to a Windows ConPTY pseudo-console.
// Older Windows releases without ConPTY (pre-1809) return errPTYUnsupported so
// the caller falls back to pipes. This gives Windows the same interactive PTY
// semantics as Unix (TUI programs, resize, ANSI) that the pipe fallback lacks.
func startAgentPTY(shell, cwd string, rows, cols int) (*agentTerminalHandle, error) {
	if !conpty.IsConPtyAvailable() {
		return nil, errPTYUnsupported
	}

	cpty, err := conpty.Start(
		quoteWindowsCommandLine(shell),
		conpty.ConPtyDimensions(cols, rows), // width=cols, height=rows
		conpty.ConPtyWorkDir(cwd),
		conpty.ConPtyEnv(agentTerminalEnv(shell)),
	)
	if err != nil {
		if errors.Is(err, conpty.ErrConPtyUnsupported) {
			return nil, errPTYUnsupported
		}
		return nil, err
	}

	pid := cpty.Pid()
	return &agentTerminalHandle{
		input:   conptyInputSink{c: cpty},
		readers: []io.Reader{cpty},
		resizer: func(r, c int) error {
			return cpty.Resize(c, r) // ConPTY Resize(width=cols, height=rows)
		},
		wait: func() (int, error) {
			code, err := cpty.Wait(context.Background())
			if err != nil {
				return -1, err
			}
			return int(code), nil
		},
		kill: func() error {
			return terminateWindowsProcess(pid)
		},
		close: func() error {
			return cpty.Close()
		},
	}, nil
}

// conptyInputSink adapts *conpty.ConPty to io.WriteCloser with a no-op Close.
// The ConPTY lifecycle is owned by the session's kill/close functors, so closing
// the input side during a forced kill must not tear the whole pseudo-console
// down out from under the wait goroutine.
type conptyInputSink struct{ c *conpty.ConPty }

func (s conptyInputSink) Write(p []byte) (int, error) { return s.c.Write(p) }
func (s conptyInputSink) Close() error                 { return nil }

// terminateWindowsProcess forcibly terminates the shell process by PID. We avoid
// conpty.Close() for the kill path because Close both terminates the process and
// releases the handles that the wait goroutine is still polling; terminating by
// PID lets Wait observe the exit cleanly before Close runs.
func terminateWindowsProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.TerminateProcess(handle, 1)
}

func quoteWindowsCommandLine(path string) string {
	if strings.ContainsAny(path, " \t") {
		return `"` + path + `"`
	}
	return path
}
