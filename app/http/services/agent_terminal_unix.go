//go:build !windows

package services

import (
	"errors"
	"io"
	"os/exec"

	"github.com/creack/pty"
)

// startAgentPTY starts the shell attached to a real pseudo-terminal using
// creack/pty. It returns errPTYUnsupported only when the platform cannot
// allocate a PTY so the caller can fall back to pipes.
func startAgentPTY(shell, cwd string, rows, cols int) (*agentTerminalHandle, error) {
	cmd := newAgentShellCommand(shell, cwd)
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		if errors.Is(err, pty.ErrUnsupported) {
			return nil, errPTYUnsupported
		}
		return nil, err
	}
	return &agentTerminalHandle{
		input:   ptyFile,
		readers: []io.Reader{ptyFile},
		resizer: func(r, c int) error {
			return pty.Setsize(ptyFile, &pty.Winsize{Rows: uint16(r), Cols: uint16(c)})
		},
		wait: func() (int, error) {
			if err := cmd.Wait(); err == nil {
				return 0, nil
			} else {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return exitErr.ExitCode(), nil
				}
				return -1, err
			}
		},
		kill: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
		close: func() error {
			return ptyFile.Close()
		},
	}, nil
}
