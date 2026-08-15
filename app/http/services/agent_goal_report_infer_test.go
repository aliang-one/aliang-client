package services

import (
	"strings"
	"testing"

	aliangmodels "aliang.one/nursorgate/app/http/models"
)

// inferGoalReportFromOutput runs only when the provider did NOT emit the
// ALIANG_GOAL_REPORT marker. The marker prompt says "omit → task is treated as
// FAILED", but for tasks WITH machine checks the optimism is tolerable because
// server-side verification backstops a false claim. For ZERO-check audit /
// research tasks there is NO verification — the report alone completes the task
// (manifest.ts:280) — so optimism manufactures false completions. The fix:
// only infer task_completed when the task has checks.
func TestInferGoalReportFromOutputRespectsChecks(t *testing.T) {
	cases := []struct {
		name                   string
		output                 string
		eventType              string
		hasChecks              bool
		wantOutcome            string
		wantCompletionProposed bool
	}{
		// THE FIX: zero-check task produced output but no marker. Must NOT
		// claim completion — there is no verification backstop.
		{"zero-check non-empty output → no_progress (no false completion)", "audited the module; see notes above", "", false, "no_progress", false},
		{"zero-check whitespace-only output → no_progress", "   \n  ", "", false, "no_progress", false},

		// Preserved: checked task non-empty output stays optimistic. Server
		// verification will catch a false claim.
		{"checked non-empty output → task_completed (verification backstops)", "ran npm test — all green", "", true, "task_completed", true},

		// Regression guards.
		{"empty output → no_progress", "", "", true, "no_progress", false},
		{"error signal → failed (regardless of checks)", "fatal: exit code 1", "", true, "failed", false},
		{"zero-check error signal → failed", "Error: command not found", "", false, "failed", false},
		{"AIError terminal event → failed", "anything", string(aliangmodels.AgentEventAIError), true, "failed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := inferGoalReportFromOutput(tc.output, tc.eventType, tc.hasChecks)
			outcome, _ := r["outcome"].(string)
			if outcome != tc.wantOutcome {
				t.Errorf("outcome: got %q want %q\nfull report: %v", outcome, tc.wantOutcome, r)
			}
			cp, _ := r["completion_proposed"].(bool)
			if cp != tc.wantCompletionProposed {
				t.Errorf("completion_proposed: got %v want %v (outcome=%q)", cp, tc.wantCompletionProposed, outcome)
			}
			// validGoalReport invariant: completion_proposed MUST equal
			// (outcome == task_completed). If this breaks, the server rejects
			// the report entirely (worse than a soft outcome).
			if cp != (outcome == "task_completed") {
				t.Errorf("completion_proposed/outcome invariant broken: cp=%v outcome=%q", cp, outcome)
			}
			// Summary must explain WHY (zero-check + no marker) so the user at
			// the sign-off gate understands the task is unverified.
			if outcome == "no_progress" && strings.TrimSpace(tc.output) != "" && !tc.hasChecks {
				summary, _ := r["summary"].(string)
				if !strings.Contains(strings.ToLower(summary), "no structured report") &&
					!strings.Contains(strings.ToLower(summary), "without a structured report") {
					t.Errorf("zero-check no-marker summary should explain the missing report; got: %q", summary)
				}
			}
		})
	}
}

// goalRequiredCheckCountFromContext reads task.requiredChecks length out of the
// goal_context envelope so attachGoalReport can pass hasChecks without
// re-parsing prose. Returns 0 for any malformed/absent envelope.
func TestGoalRequiredCheckCountFromContext(t *testing.T) {
	cases := []struct {
		name string
		raw  interface{}
		want int
	}{
		{"nil", nil, 0},
		{"wrong type", "not a map", 0},
		{"envelope without task", map[string]interface{}{"goal": map[string]interface{}{}}, 0},
		{"task without requiredChecks", map[string]interface{}{"task": map[string]interface{}{"key": "T1"}}, 0},
		{"empty requiredChecks", map[string]interface{}{"task": map[string]interface{}{"requiredChecks": []interface{}{}}}, 0},
		{"two checks", map[string]interface{}{"task": map[string]interface{}{
			"requiredChecks": []interface{}{
				map[string]interface{}{"key": "a", "type": "command"},
				map[string]interface{}{"key": "b", "type": "file_contains"},
			},
		}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := goalRequiredCheckCountFromContext(tc.raw); got != tc.want {
				t.Errorf("got %d want %d", got, tc.want)
			}
		})
	}
}
