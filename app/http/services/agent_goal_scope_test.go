package services

import "testing"

func TestGoalAllowedScopeFromContext(t *testing.T) {
	envelope := map[string]interface{}{
		"task": map[string]interface{}{
			"allowedRoots":    []interface{}{"/ws/src/foo", " /ws/x "},
			"allowedCommands": []interface{}{"git", "npm", "  "},
		},
	}
	roots := goalAllowedRootsFromContext(envelope)
	wantRoots := []string{"/ws/src/foo", "/ws/x"}
	if len(roots) != len(wantRoots) {
		t.Fatalf("roots: got %v want %v", roots, wantRoots)
	}
	for i := range wantRoots {
		if roots[i] != wantRoots[i] {
			t.Errorf("roots[%d]: got %q want %q", i, roots[i], wantRoots[i])
		}
	}
	cmds := goalAllowedCommandsFromContext(envelope)
	wantCmds := []string{"git", "npm"}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("cmds: got %v want %v", cmds, wantCmds)
	}
	// 畸形/缺失 → nil（不强制）
	if r := goalAllowedRootsFromContext(nil); r != nil {
		t.Errorf("nil envelope should give nil roots, got %v", r)
	}
	if c := goalAllowedCommandsFromContext("not a map"); c != nil {
		t.Errorf("wrong type should give nil cmds, got %v", c)
	}
}

func TestGoalScopeAllowsToolCallWritePaths(t *testing.T) {
	roots := []string{"/ws/src/foo"}
	raw := func(filePath string) map[string]interface{} {
		return map[string]interface{}{"tool_input": map[string]interface{}{"file_path": filePath}}
	}
	cases := []struct {
		name  string
		tool  string
		path  string
		allow bool
	}{
		{"in root abs", "Edit", "/ws/src/foo/a.ts", true},
		{"outside root", "Edit", "/ws/src/bar/b.ts", false},
		{"parent escape", "Write", "/ws/src/foo/../../etc/passwd", false},
		{"relative resolved under projectPath", "Edit", "src/foo/c.ts", true},
		{"relative resolved outside", "Edit", "src/bar/d.ts", false},
		{"new file (EvalSymlinks fails) under root", "Write", "/ws/src/foo/new.ts", true},
		{"empty roots -> no enforcement", "Edit", "/anywhere", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := roots
			if tc.name == "empty roots -> no enforcement" {
				rs = nil
			}
			ok, reason := goalScopeAllowsToolCall(tc.tool, raw(tc.path), "/ws", rs, nil)
			if ok != tc.allow {
				t.Errorf("got allow=%v want %v (reason=%q)", ok, tc.allow, reason)
			}
		})
	}
}

func TestGoalScopeAllowsToolCallBash(t *testing.T) {
	cmds := []string{"git", "npm", "npx"}
	raw := func(cmd string) map[string]interface{} {
		return map[string]interface{}{"tool_input": map[string]interface{}{"command": cmd}}
	}
	cases := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"single allowed", "npm test", true},
		{"single not allowed", "rm -rf /", false},
		{"compound all allowed", "npm test && npm run lint", true},
		{"compound with bad segment", "npm test; rm -rf x", false},
		{"pipe both allowed", "npm test | npm run lint", true},
		{"empty command", "", true},
		{"empty allowedCommands -> no enforcement", "anything goes", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := cmds
			if tc.name == "empty allowedCommands -> no enforcement" {
				cs = nil
			}
			ok, reason := goalScopeAllowsToolCall("Bash", raw(tc.cmd), "/ws", nil, cs)
			if ok != tc.allow {
				t.Errorf("got allow=%v want %v (reason=%q)", ok, tc.allow, reason)
			}
		})
	}
}

func TestGoalScopeAllowsToolCallReadonlyPassthrough(t *testing.T) {
	ok, _ := goalScopeAllowsToolCall("Read", map[string]interface{}{}, "/ws", []string{"/ws/src"}, []string{"git"})
	if !ok {
		t.Error("Read tool should pass even with scope set")
	}
	ok, _ = goalScopeAllowsToolCall("Edit", map[string]interface{}{"tool_input": map[string]interface{}{"file_path": "/anywhere"}}, "/ws", nil, nil)
	if !ok {
		t.Error("empty total scope must not enforce")
	}
}

func TestGoalScopePlumbingRunToSession(t *testing.T) {
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID:               "r1",
		goalIdentity:        testGoalIdentity(),
		goalAllowedRoots:    []string{"/ws/src/foo"},
		goalAllowedCommands: []string{"npm"},
	}, func(interface{}) error { return nil })
	if len(emitter.goalAllowedRoots) != 1 || emitter.goalAllowedRoots[0] != "/ws/src/foo" {
		t.Errorf("emitter roots not mirrored: %v", emitter.goalAllowedRoots)
	}
	if len(emitter.goalAllowedCommands) != 1 || emitter.goalAllowedCommands[0] != "npm" {
		t.Errorf("emitter commands not mirrored: %v", emitter.goalAllowedCommands)
	}
}
