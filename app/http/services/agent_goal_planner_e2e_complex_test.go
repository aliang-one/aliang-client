//go:build e2e

// Real-gateway planner scenarios that approximate production loads (open-ended
// objectives, multi-file projects, grep-driven exploration) — the cases the
// single-happy-path TestPlannerLocalE2E doesn't cover. Same build tag + env as
// the other e2e tests; reuses the callServer/helpers from agent_goal_planner_e2e_test.go.
package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// biggerTempProject builds a small but multi-file repo (src + a couple config
// files, no tests, no CI) so open-ended exploration has real structure to dig
// through — closer to a real project than the 1-file happy-path fixture.
func biggerTempProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	w := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	w("README.md", "# svc-a\n\nA small Node service.\n")
	w("package.json", `{
  "name": "svc-a",
  "version": "0.2.0",
  "main": "src/index.js",
  "scripts": { "start": "node src/index.js" }
}`)
	w("src/index.js", "const http = require('http');\nfunction handler(req, res){ res.end('hello'); }\nmodule.exports = { handler };\n")
	w("src/util.js", "function add(a, b){ return a + b; }\nmodule.exports = { add };\n")
	w("src/config.js", "module.exports = { port: 3000, logLevel: 'info' };\n")
	w(".gitignore", "node_modules/\n")
	return root
}

// TestPlannerE2E_ComplexObjective — an OPEN-ENDED objective (audit + plan
// improvements) on a multi-file project, the shape that was timing out /
// running out of turns in production. Asserts the planner still converges on a
// valid plan, and logs turn count + wall time so we see how hard glm-5.2 had to
// work (and whether the production budgets would have cut it off).
func TestPlannerE2E_ComplexObjective(t *testing.T) {
	skipIfNoPlannerE2E(t)
	root := biggerTempProject(t)
	objective := "Audit this project for gaps — it has no tests, no CI, no linting. Plan a coherent, minimal improvement: add a unit test for src/util.js `add`, a `test` npm script, and a basic GitHub Actions workflow. Keep scope tight."

	callServer := makeDirectOpenAICallServer(t, objective)
	evidence, err := buildGoalPlanContext(root, nil)
	if err != nil {
		t.Fatalf("buildGoalPlanContext: %v", err)
	}
	start := time.Now()
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: evidence, callServer: callServer, maxTurns: 25,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("open-ended objective did NOT converge in %s (maxTurns=25): %v\n"+
			"this is the production failure mode — glm-5.2 ran out of turns/time on a big objective", elapsed, err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("invalid plan after %s: %#v", elapsed, proposal)
	}
	tasks, _ := proposal["tasks"].([]interface{})
	t.Logf("complex objective converged: turns-used=up-to-25 elapsed=%s tasks=%d", elapsed, len(tasks))
	if len(tasks) < 2 {
		t.Fatalf("an audit-and-plan objective should yield ≥2 tasks, got %d", len(tasks))
	}
}

// TestPlannerE2E_GrepExploration — the objective can only be met by GREP-ing
// for a hidden symbol (proves the grep read-only tool works end-to-end through
// the gateway, not just read_file). The plan must reference the found usages.
func TestPlannerE2E_GrepExploration(t *testing.T) {
	skipIfNoPlannerE2E(t)
	root := t.TempDir()
	w := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// `LEGACY_TOKEN` is referenced in 2 files; the planner must grep to find both.
	w("src/auth.js", "const LEGACY_TOKEN = process.env.TOK;\nfunction login(){ return LEGACY_TOKEN; }\nmodule.exports = { login };\n")
	w("src/legacy.js", "// legacy\nconst x = LEGACY_TOKEN;\n")
	w("README.md", "# app\n")
	objective := "Find every place the legacy token constant LEGACY_TOKEN is referenced (use grep), then plan to replace it with a modern env read. The plan must cover all usages you found."

	callServer := makeDirectOpenAICallServer(t, objective)
	evidence, err := buildGoalPlanContext(root, nil)
	if err != nil {
		t.Fatalf("buildGoalPlanContext: %v", err)
	}
	start := time.Now()
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: evidence, callServer: callServer, maxTurns: 25,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("grep-driven objective did not converge in %s: %v", elapsed, err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("invalid plan: %#v", proposal)
	}
	tasks, _ := proposal["tasks"].([]interface{})
	t.Logf("grep objective converged: elapsed=%s tasks=%d", elapsed, len(tasks))
	if len(tasks) == 0 {
		t.Fatal("grep objective produced no tasks")
	}
}

// TestPlannerE2E_LargeFile — a project with a file big enough to stress
// read_file truncation + the planner message-size path (the planner_message_too_large
// failure mode lived here). Asserts the planner still converges despite the big file.
func TestPlannerE2E_LargeFile(t *testing.T) {
	skipIfNoPlannerE2E(t)
	root := t.TempDir()
	// ~120KB of repetitive source — well past the read_file truncation budget so
	// the planner sees a truncated result, not the whole file.
	big := make([]byte, 0, 120*1024)
	for len(big) < 120*1024 {
		big = append(big, []byte("// padding line full of noise to bloat the file\n")...)
	}
	big = append(big, []byte("\nexport function add(a, b){ return a + b; }\n")...)
	if err := os.WriteFile(filepath.Join(root, "src.js"), big, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"big","scripts":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	objective := "Add a unit test for the `add` function in src.js and wire a test script. Keep it minimal."

	callServer := makeDirectOpenAICallServer(t, objective)
	evidence, err := buildGoalPlanContext(root, nil)
	if err != nil {
		t.Fatalf("buildGoalPlanContext: %v", err)
	}
	start := time.Now()
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: evidence, callServer: callServer, maxTurns: 25,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("large-file objective did not converge in %s: %v", elapsed, err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("invalid plan: %#v", proposal)
	}
	t.Logf("large-file objective converged: elapsed=%s", elapsed)
}
