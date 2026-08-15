package services

import (
	"strings"
	"testing"
)

// serializeGoalContext must render the FULL goal_context envelope the server
// builds (context.ts: buildGoalContext). The server explicitly ships
// constraints / nonGoals / plan DAG / goalProgress / budget so the executor
// sees the approved contract, the big picture, and why prior attempts failed.
// An earlier version silently dropped everything except objective + current
// task + queuedInputs — defeating that intent. These tests pin every field.
func TestSerializeGoalContextRendersFullEnvelope(t *testing.T) {
	envelope := map[string]interface{}{
		"version": float64(1),
		"goal": map[string]interface{}{
			"objective":     "Ship the foo feature",
			"revisionNumber": float64(3),
			"taskAttempt":    float64(1),
			"constraints":    []interface{}{"must not break the public API", "stay under 5000 tokens"},
			"nonGoals":       []interface{}{"do not refactor the bar module"},
			"driver":         "server",
		},
		"task": map[string]interface{}{
			"key":              "T1",
			"title":            "Implement foo endpoint",
			"description":      "add POST /foo returning the widget",
			"allowedRoots":     []interface{}{"src/foo"},
			"allowedCommands":  []interface{}{"npm test"},
			"requiredChecks": []interface{}{
				map[string]interface{}{"id": "c1", "key": "foo-unit-test", "type": "command", "command": "npm run test:foo"},
				map[string]interface{}{"id": "c2", "key": "foo-banner", "type": "file_contains", "path": "src/foo/README.md", "contains": "foo is wired"},
			},
		},
		"budget": map[string]interface{}{
			"tokenBudget": float64(5000),
		},
		"queuedInputs": []interface{}{
			map[string]interface{}{"sequence": float64(1), "kind": "clarify", "content": "prefer JSON responses"},
		},
		"goalProgress": map[string]interface{}{
			"completedTasks": float64(2),
			"totalTasks":     float64(5),
			"remainingTasks": []interface{}{
				map[string]interface{}{"key": "T3", "title": "Document foo"},
			},
			"recentFailures": []interface{}{
				map[string]interface{}{"taskKey": "T-prev", "summary": "npm test failed: missing dep"},
			},
		},
		"plan": []interface{}{
			map[string]interface{}{
				"key":            "T1",
				"title":          "Implement foo endpoint",
				"state":          "in_progress",
				"dependsOn":      []interface{}{},
				"requiredChecks": float64(1),
			},
			map[string]interface{}{
				"key":            "T2",
				"title":          "Wire foo e2e",
				"state":          "pending",
				"dependsOn":      []interface{}{"T1"},
				"requiredChecks": float64(1),
			},
		},
	}

	out := serializeGoalContext(envelope)

	cases := []struct {
		name        string
		needle      string
		explanation string
	}{
		// --- Gap A1: previously-dropped envelope fields ---
		{"constraints rendered", "must not break the public API", "constraints are the approved contract; executor must see them"},
		{"second constraint rendered", "stay under 5000 tokens", "all constraints, not just the first"},
		{"nonGoals rendered", "do not refactor the bar module", "nonGoals fence the executor; dropping them invites scope creep"},
		{"plan sibling task title", "Wire foo e2e", "executor must see downstream tasks it must enable"},
		{"plan task state", "in_progress", "plan state lets executor see what's done / in-flight"},
		{"goalProgress remaining task", "Document foo", "distance-to-goal must reach the executor"},
		{"goalProgress recent failure", "npm test failed: missing dep", "prior-failure context breaks repeated blind failures"},
		{"budget field", "tokenBudget", "budget visibility lets the executor self-throttle"},

		// --- Regression: fields that already rendered must keep rendering ---
		{"objective", "Ship the foo feature", "objective is the anchor"},
		{"task title", "Implement foo endpoint", "current task title"},
		{"task description", "add POST /foo returning the widget", "current task description"},
		{"allowed roots", "src/foo", "allowed roots scope hint"},
		{"allowed commands", "npm test", "allowed commands scope hint"},
		{"required check key", "foo-unit-test", "required check key"},
		{"required check command", "npm run test:foo", "check command tells executor what must actually pass"},
		{"required check file_contains path", "src/foo/README.md", "file_contains path tells executor which file"},
		{"required check file_contains contains", "foo is wired", "file_contains substring the executor must make true"},
		{"queued input", "prefer JSON responses", "queued user clarification"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(out, tc.needle) {
				t.Errorf("serializeGoalContext output missing %q (%s).\nOutput:\n%s", tc.needle, tc.explanation, out)
			}
		})
	}
}

// Empty/missing optional fields must not crash and must not emit empty headers.
func TestSerializeGoalContextOmitsEmptySectionsGracefully(t *testing.T) {
	out := serializeGoalContext(map[string]interface{}{
		"goal": map[string]interface{}{"objective": "solo objective"},
		"task": map[string]interface{}{"key": "T0", "title": "lone task"},
	})
	if !strings.Contains(out, "solo objective") {
		t.Errorf("objective not rendered: %s", out)
	}
	for _, ghost := range []string{"Constraints", "Non-goals", "Plan", "Progress", "Budget"} {
		if strings.Contains(out, ghost) {
			t.Errorf("empty section header %q should not appear when the field is absent; output:\n%s", ghost, out)
		}
	}
}
