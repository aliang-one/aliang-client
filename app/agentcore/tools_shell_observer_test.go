//go:build !windows

package agentcore

import (
	"context"
	"path/filepath"
	"testing"
)

func TestShellObserverReportsProcessOutcome(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		outcome     ShellOutcome
		missingExec bool
		cancel      bool
		timeoutMS   float64
	}{
		{name: "success", command: "exit 0", outcome: "completed"},
		{name: "failure", command: "exit 7", outcome: "failed"},
		{name: "cancelled", command: "sleep 5", outcome: "cancelled", cancel: true},
		{name: "timed out", command: "sleep 5", outcome: "timed_out", timeoutMS: 100},
		{name: "launch failure", command: "exit 0", missingExec: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			policy := shPolicy()
			if tt.missingExec {
				policy.path = filepath.Join(t.TempDir(), "missing-shell")
			}
			var started string
			var ended ShellOutcome
			env := &ToolEnv{
				ShellPolicy: policy,
				OnShellStart: func(tool string) func(ShellOutcome) {
					started = tool
					if tt.cancel {
						cancel()
					}
					return func(outcome ShellOutcome) { ended = outcome }
				},
			}

			got := (&bashTool{}).Execute(ctx, map[string]interface{}{
				"command": tt.command, "timeout": tt.timeoutMS,
			}, env)
			if tt.missingExec {
				if started != "" || ended != "" || !got.IsError {
					t.Fatalf("launch failure: started=%q ended=%q result=%+v", started, ended, got)
				}
				return
			}
			if started != "Bash" || ended != tt.outcome || got.IsError != (tt.outcome != "completed") {
				t.Fatalf("started=%q ended=%q result=%+v, want Bash %q", started, ended, got, tt.outcome)
			}
		})
	}
}
