package services

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandNameFromRel(t *testing.T) {
	cases := map[string]string{
		"speckit.plan.md": "speckit.plan",
		"foo/bar.md":      "foo.bar",
		"a/b/c.md":        "a.b.c",
		"single.md":       "single",
	}
	for in, want := range cases {
		if got := commandNameFromRel(in); got != want {
			t.Errorf("commandNameFromRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPluginShortName(t *testing.T) {
	cases := map[string]string{
		"plugin-dev@claude-plugins-official": "plugin-dev",
		"superpowers@x":                      "superpowers",
		"noprefix":                           "noprefix",
		"":                                   "",
		"@onlymarket":                        "",
	}
	for in, want := range cases {
		if got := pluginShortName(in); got != want {
			t.Errorf("pluginShortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPluginRecordEnabled(t *testing.T) {
	cases := []struct {
		rec  installedPluginRecord
		want bool
	}{
		{installedPluginRecord{Scope: "user"}, true},
		{installedPluginRecord{Scope: ""}, true},                            // absent scope = treated enabled
		{installedPluginRecord{Scope: "user", Disabled: true}, false},       // explicitly disabled
		{installedPluginRecord{Scope: "project", ProjectPath: "/x"}, false}, // project scope skipped
	}
	for _, c := range cases {
		if got := pluginRecordEnabled(c.rec); got != c.want {
			t.Errorf("pluginRecordEnabled(%+v) = %v, want %v", c.rec, got, c.want)
		}
	}
}

func TestRemoteBool(t *testing.T) {
	cases := []struct {
		name     string
		msg      map[string]interface{}
		fallback bool
		want     bool
	}{
		{"missing", map[string]interface{}{}, true, true},
		{"nil", map[string]interface{}{"flag": nil}, true, true},
		{"json-bool-true", map[string]interface{}{"flag": true}, false, true},
		{"json-bool-false", map[string]interface{}{"flag": false}, true, false},
		{"str-true", map[string]interface{}{"flag": "true"}, false, true},
		{"str-1", map[string]interface{}{"flag": "1"}, false, true},
		{"str-yes", map[string]interface{}{"flag": "YES"}, false, true},
		{"str-false", map[string]interface{}{"flag": "false"}, true, false},
		{"str-empty-uses-fallback", map[string]interface{}{"flag": ""}, true, true},
		{"num-nonzero", map[string]interface{}{"flag": float64(1)}, false, true},
		{"num-zero", map[string]interface{}{"flag": float64(0)}, true, false},
	}
	for _, c := range cases {
		if got := remoteBool(c.msg, "flag", c.fallback); got != c.want {
			t.Errorf("%s: remoteBool = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSplitFrontmatterField(t *testing.T) {
	cases := []struct {
		line      string
		wantKey   string
		wantValue string
		wantOk    bool
	}{
		{"description: hello", "description", "hello", true},
		{"Description: Mixed Case", "description", "Mixed Case", true},
		{`name: "quoted value"`, "name", "quoted value", true},
		{"- list: item", "", "", false},
		{"#comment", "", "", false},
		{"", "", "", false},
		{"nocolon", "", "", false},
		{"value: with: colons", "value", "with: colons", true},
	}
	for _, c := range cases {
		key, value, ok := splitFrontmatterField(c.line)
		if key != c.wantKey || value != c.wantValue || ok != c.wantOk {
			t.Errorf("splitFrontmatterField(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.line, key, value, ok, c.wantKey, c.wantValue, c.wantOk)
		}
	}
}

func TestParseSlashFrontmatter(t *testing.T) {
	dir := t.TempDir()

	cmdPath := filepath.Join(dir, "cmd.md")
	if err := os.WriteFile(cmdPath, []byte("---\ndescription: Plans stuff\nargument-hint: <feature>\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm, ok := parseSlashFrontmatter(cmdPath)
	if !ok {
		t.Fatal("expected ok=true for readable file")
	}
	if fm.description != "Plans stuff" {
		t.Errorf("description = %q, want %q", fm.description, "Plans stuff")
	}
	if fm.argumentHint != "<feature>" {
		t.Errorf("argumentHint = %q, want %q", fm.argumentHint, "<feature>")
	}

	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: my-skill\ndescription: Does things\nargument_hint: [x]\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm2, ok := parseSlashFrontmatter(skillPath)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if fm2.name != "my-skill" {
		t.Errorf("name = %q, want %q", fm2.name, "my-skill")
	}
	if fm2.argumentHint != "[x]" {
		t.Errorf("underscore argument_hint = %q, want %q", fm2.argumentHint, "[x]")
	}

	plain := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, []byte("just body, no fences"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm3, ok := parseSlashFrontmatter(plain)
	if !ok {
		t.Fatal("expected ok=true for no-frontmatter file")
	}
	if fm3.description != "" || fm3.name != "" {
		t.Errorf("expected zero frontmatter, got %+v", fm3)
	}

	if _, ok = parseSlashFrontmatter(filepath.Join(dir, "nope.md")); ok {
		t.Fatal("expected ok=false for missing file")
	}
}

func TestScanCommandMarkdownsNamespacing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "create-plugin.md"), []byte("---\ndescription: x\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested.md"), []byte("---\ndescription: y\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := scanCommandMarkdowns(root, "user", "plugin", root, "plugin-dev:", "claude")
	names := map[string]bool{}
	for _, e := range entries {
		names[e["name"].(string)] = true
		if e["origin"] != "plugin" || e["scope"] != "user" {
			t.Errorf("expected plugin/user, got scope=%v origin=%v", e["scope"], e["origin"])
		}
		if e["provider"] != "claude" {
			t.Errorf("expected claude provider tag, got %v", e["provider"])
		}
	}
	if !names["plugin-dev:create-plugin"] {
		t.Errorf("missing namespaced command, got %v", names)
	}
	if !names["plugin-dev:sub.nested"] {
		t.Errorf("missing nested namespaced command, got %v", names)
	}
}

func TestCollectProjectSlashCommands(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, ".claude", "commands")
	if err := os.MkdirAll(filepath.Join(cmdDir, "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "speckit.plan.md"), []byte("---\ndescription: plan stuff\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "foo", "bar.md"), []byte("---\ndescription: bar\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, ".claude", "skills", "ui-ux-pro-max")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: ui-ux-pro-max\ndescription: ui skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := collectProjectSlashCommands(root)
	got := map[string]map[string]interface{}{}
	for _, e := range entries {
		got[e["name"].(string)] = e
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 commands, got %d: %+v", len(got), got)
	}
	for _, name := range []string{"speckit.plan", "foo.bar", "ui-ux-pro-max"} {
		e, ok := got[name]
		if !ok {
			t.Errorf("missing %s", name)
			continue
		}
		if e["scope"] != "project" || e["origin"] != "project" {
			t.Errorf("%s: scope=%v origin=%v", name, e["scope"], e["origin"])
		}
	}
	// skill with no name field falls back to directory name
	root2 := t.TempDir()
	sd := filepath.Join(root2, ".claude", "skills", "fallback-skill")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte("---\ndescription: no name field\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	es := collectProjectSlashCommands(root2)
	if len(es) != 1 || es[0]["name"] != "fallback-skill" {
		t.Errorf("expected fallback-skill, got %+v", es)
	}
}

func TestSortSlashCommands(t *testing.T) {
	cmds := []map[string]interface{}{
		{"name": "z", "origin": "plugin"},
		{"name": "a", "origin": "user"},
		{"name": "b", "origin": "project"},
		{"name": "a", "origin": "project"},
	}
	sortSlashCommands(cmds)
	got := []string{}
	for _, c := range cmds {
		got = append(got, c["origin"].(string)+":"+c["name"].(string))
	}
	want := []string{"project:a", "project:b", "user:a", "plugin:z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestNormalizeSlashProvider(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"codex":      "codex",
		"CODEX":      "codex",
		"  Codex  ":  "codex",
		"claude":     "claude",
		"claudecode": "claude",
		"ClaudeCode": "claude",
		"open-code":  "opencode",
		"open_code":  "opencode",
		"OpenCode":   "opencode",
		"unknown":    "",
	}
	for in, want := range cases {
		if got := normalizeSlashProvider(in); got != want {
			t.Errorf("normalizeSlashProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexBuiltinCommands(t *testing.T) {
	cmds := codexBuiltinCommands()
	if len(cmds) < 10 {
		t.Fatalf("expected a sizable codex builtin baseline, got %d", len(cmds))
	}
	names := map[string]bool{}
	for _, c := range cmds {
		if c["provider"] != "codex" {
			t.Errorf("builtin %v provider=%v want codex", c["name"], c["provider"])
		}
		if c["scope"] != "builtin" || c["origin"] != "builtin" {
			t.Errorf("builtin %v scope=%v origin=%v want builtin", c["name"], c["scope"], c["origin"])
		}
		if d, _ := c["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("builtin %v has empty description", c["name"])
		}
		names[c["name"].(string)] = true
	}
	for _, want := range []string{"model", "plan", "goal", "review", "diff", "clear", "compact", "status", "permissions"} {
		if !names[want] {
			t.Errorf("missing codex builtin %q", want)
		}
	}
}

func TestCollectCodexPromptCommands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "refactor.md"), []byte("---\ndescription: Refactor selected code\nargument-hint: <file>\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lint.md"), []byte("plain body, no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := collectCodexPromptCommands(dir)
	got := map[string]map[string]interface{}{}
	for _, c := range cmds {
		got[c["name"].(string)] = c
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 codex prompts, got %d: %+v", len(got), got)
	}
	ref, ok := got["refactor"]
	if !ok {
		t.Fatal("missing refactor prompt")
	}
	if ref["description"] != "Refactor selected code" {
		t.Errorf("refactor description=%v", ref["description"])
	}
	if ref["arg_hint"] != "<file>" {
		t.Errorf("refactor arg_hint=%v", ref["arg_hint"])
	}
	if ref["provider"] != "codex" || ref["scope"] != "user" {
		t.Errorf("refactor provider=%v scope=%v want codex/user", ref["provider"], ref["scope"])
	}
	if _, ok := got["lint"]; !ok {
		t.Error("missing lint prompt (name should fall back to filename)")
	}

	// Missing directory must not panic and yields nothing.
	if got := collectCodexPromptCommands(filepath.Join(dir, "does-not-exist")); len(got) != 0 {
		t.Errorf("missing dir should yield no commands, got %d", len(got))
	}
}

func TestCollectOpenCodeSlashCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()

	projectCmdDir := filepath.Join(project, ".opencode", "commands")
	if err := os.MkdirAll(projectCmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectCmdDir, "ship.md"), []byte("---\ndescription: Ship it\nargument-hint: <target>\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	userCmdDir := filepath.Join(home, ".config", "opencode", "commands")
	if err := os.MkdirAll(userCmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userCmdDir, "daily.md"), []byte("---\ndescription: Daily work\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := collectOpenCodeSlashCommands(project, true)
	got := map[string]map[string]interface{}{}
	for _, c := range cmds {
		got[c["name"].(string)] = c
		if c["provider"] != "opencode" {
			t.Errorf("%v provider=%v want opencode", c["name"], c["provider"])
		}
	}
	for _, want := range []string{"init", "help", "model", "undo", "redo", "ship", "daily"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing opencode command %q; got %+v", want, got)
		}
	}
	if got["ship"]["scope"] != "project" || got["ship"]["arg_hint"] != "<target>" {
		t.Errorf("ship command = %+v", got["ship"])
	}
	if got["daily"]["scope"] != "user" {
		t.Errorf("daily command = %+v", got["daily"])
	}
}

func TestSlashCommandEntryProviderTag(t *testing.T) {
	e := slashCommandEntry("foo", "d", "<x>", "user", "user", "s", "claude")
	if e["provider"] != "claude" {
		t.Errorf("provider=%v want claude", e["provider"])
	}
}

func TestCollectProjectSlashCommandsProviderTag(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, ".claude", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "demo.md"), []byte("---\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range collectProjectSlashCommands(root) {
		if e["provider"] != "claude" {
			t.Errorf("project command provider=%v want claude", e["provider"])
		}
	}
}
