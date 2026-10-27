//go:build darwin

package tccguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attendance heuristics. A TCC prompt is only ever raised when a human is
// plausibly in front of the Mac:
//
//   - screen locked (kCGSSessionScreenIsLocked) → unattended;
//   - login window / no console user / console user is root → unattended;
//   - fast user switching: our process is not the foreground account → it must
//     never raise a prompt on someone else's desktop (also the privacy rule:
//     never request another account's folders);
//   - HID idle longer than attendanceIdle (5 min) → unattended.
const (
	attendanceCacheTTL = 5 * time.Second
	attendanceIdle     = 5 * time.Minute
	attendanceToolWait = 3 * time.Second
)

var (
	attendanceMu   sync.Mutex
	attendanceAt   time.Time
	attendanceSeen bool
)

func platformSupported() bool { return true }

func platformAttended() bool {
	attendanceMu.Lock()
	defer attendanceMu.Unlock()
	if !attendanceAt.IsZero() && time.Since(attendanceAt) < attendanceCacheTTL {
		return attendanceSeen
	}
	attended := detectAttended()
	attendanceAt = time.Now()
	attendanceSeen = attended
	return attended
}

var (
	consoleNamePattern  = regexp.MustCompile(`(?m)^\s*Name\s*:\s*(\S+)\s*$`)
	consoleUIDPattern   = regexp.MustCompile(`(?m)^\s*UID\s*:\s*(\d+)\s*$`)
	screenLockedPattern = regexp.MustCompile(`kCGSSessionScreenIsLocked\s*:\s*TRUE`)
	hidIdlePattern      = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)
)

type consoleSession struct {
	name   string
	uid    int
	locked bool
	ok     bool
}

func detectAttended() bool {
	session := readConsoleSession()
	if !session.ok || session.locked {
		return false
	}
	name := strings.TrimSpace(session.name)
	if name == "" || strings.EqualFold(name, "loginwindow") || strings.EqualFold(name, "root") {
		return false
	}
	// Fast user switching: the console foreground belongs to another account.
	// Our consent prompt would not be visible to anyone, and asking for that
	// account's folders would violate the privacy rule. A root process acts on
	// behalf of the console user, so the restriction does not apply to it.
	if os.Getuid() != 0 && session.uid != os.Getuid() {
		return false
	}
	idle, err := hidIdleTime()
	if err != nil {
		// Cannot confirm somebody is present — stay conservative and refuse
		// rather than risk an unanswerable prompt.
		return false
	}
	return idle < attendanceIdle
}

func readConsoleSession() consoleSession {
	out, err := runSystemTool("/usr/sbin/scutil", "show State:/Users/ConsoleUser\n")
	if err != nil {
		return consoleSession{}
	}
	text := string(out)
	session := consoleSession{ok: true, locked: screenLockedPattern.MatchString(text)}
	if m := consoleNamePattern.FindStringSubmatch(text); m != nil {
		session.name = m[1]
	}
	if m := consoleUIDPattern.FindStringSubmatch(text); m != nil {
		if uid, err := strconv.Atoi(m[1]); err == nil {
			session.uid = uid
		}
	}
	return session
}

func hidIdleTime() (time.Duration, error) {
	out, err := runSystemTool("/usr/sbin/ioreg", "", "-c", "IOHIDSystem")
	if err != nil {
		return 0, err
	}
	m := hidIdlePattern.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("HIDIdleTime not found in ioreg output")
	}
	nanos, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(nanos), nil
}

func runSystemTool(path string, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), attendanceToolWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Output()
}
