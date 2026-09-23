package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func atoiPid(t *testing.T, raw string) int {
	t.Helper()
	pid, err := strconv.Atoi(raw)
	require.NoError(t, err)
	return pid
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s never appeared within %v", path, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func syscallSignalZero() syscall.Signal {
	return syscall.Signal(0)
}

// The effort probe must point CLAUDE_CONFIG_DIR at a throwaway dir: the
// sentinel args only insta-exit on old CLI generations — on 2.1.280+ the CLI
// warns and continues, ran a REAL model turn inside ~/.claude/projects, and the
// inventory scan imported it as a phone conversation (2026-09-23 incident).
func TestProbeClaudeEffortLevelsIsolatesConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixture is unix-only")
	}
	_, err := exec.LookPath("bash")
	require.NoError(t, err)

	capture := filepath.Join(t.TempDir(), "captured_env")
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	require.NoError(t, os.WriteFile(script, []byte(
		"#!/bin/bash\n"+
			"env | grep '^CLAUDE_CONFIG_DIR=' > "+capture+" || true\n"+
			"echo \"Warning: Unknown --effort value '__aliang_probe__' — ignoring it. Valid values: low, medium, high, xhigh, max.\"\n"+
			"exit 0\n",
	), 0o755))

	levels := probeClaudeEffortLevels(script)
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, levels)

	raw, err := os.ReadFile(capture)
	require.NoError(t, err, "fake CLI must observe CLAUDE_CONFIG_DIR in its environment")
	configDir := strings.TrimSpace(strings.TrimPrefix(string(raw), "CLAUDE_CONFIG_DIR="))
	require.NotEmpty(t, configDir)
	// The throwaway dir must be cleaned up after the probe returns.
	_, statErr := os.Stat(configDir)
	assert.True(t, os.IsNotExist(statErr), "probe config dir %s must be removed after the probe", configDir)
}

// A plain CommandContext kill only reaps the direct child; a CLI that spawns a
// background child (or re-execs) keeps running a real model turn after the 5s
// probe deadline. The helper must kill the whole process group.
func TestNewBackgroundCommandContextKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill is unix-only")
	}
	_, err := exec.LookPath("bash")
	require.NoError(t, err)

	dir := t.TempDir()
	childPidFile := filepath.Join(dir, "child.pid")
	readyMarker := filepath.Join(dir, "ready")
	script := filepath.Join(dir, "spawner.sh")
	require.NoError(t, os.WriteFile(script, []byte(
		"#!/bin/bash\n"+
			"sleep 30 &\n"+
			"echo $! > "+childPidFile+"\n"+
			"touch "+readyMarker+"\n"+
			"wait\n",
	), 0o755))

	// Cancel manually only AFTER the spawner recorded the child pid — a ctx
	// deadline could otherwise fire before the script wrote the pid file and
	// race the test setup itself.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newBackgroundCommandContext(ctx, script)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.CombinedOutput()
		close(done)
	}()
	waitForFile(t, readyMarker, 5*time.Second)

	started := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CombinedOutput did not return within 10s of cancel")
	}
	elapsed := time.Since(started)

	// The background child inherits stdout, so without a group kill Wait hangs
	// on pipe EOF until the child exits naturally (the production probe hung
	// 2m13s instead of returning at its 5s deadline). Group kill ⇒ prompt EOF.
	assert.Less(t, elapsed, 5*time.Second,
		"CombinedOutput must return promptly after cancel, got %v", elapsed)

	raw, err := os.ReadFile(childPidFile)
	require.NoError(t, err, "spawner must have recorded the background child pid")
	pid := strings.TrimSpace(string(raw))
	require.NotEmpty(t, pid)

	// The orphaned child must not outlive the killed group.
	deadline := time.Now().Add(3 * time.Second)
	for {
		check, checkErr := os.FindProcess(atoiPid(t, pid))
		if checkErr == nil {
			if sigErr := check.Signal(syscallSignalZero()); sigErr != nil {
				break // process gone
			}
		} else {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child %s survived the process-group kill", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
