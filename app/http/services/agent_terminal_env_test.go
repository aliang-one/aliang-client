package services

import (
	"strings"
	"testing"
)

// ---- expandWindowsPercentVars:展开 %VAR% 引用,未知变量保留字面量 ----

func TestExpandWindowsPercentVarsExpandsKnownVars(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "APPDATA" {
			return `C:\Users\u\AppData\Roaming`, true
		}
		return "", false
	}
	got := expandWindowsPercentVars(`%APPDATA%\npm`, lookup)
	want := `C:\Users\u\AppData\Roaming\npm`
	if got != want {
		t.Errorf("展开结果 %q，期望 %q", got, want)
	}
}

func TestExpandWindowsPercentVarsKeepsUnknownLiteral(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	got := expandWindowsPercentVars(`%NOT_SET_XYZ%\npm`, lookup)
	if got != `%NOT_SET_XYZ%\npm` {
		t.Errorf("未知变量应保留字面量，got %q", got)
	}
}

func TestExpandWindowsPercentVarsMultipleAndNoVars(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "A" {
			return "1", true
		}
		return "", false
	}
	cases := []struct{ in, want string }{
		{`%A%;%A%;%B%`, `1;1;%B%`},
		{`C:\plain\path`, `C:\plain\path`},
		{"", ""},
		{`%A%`, "1"},
		{`%`, `%`},   // 无配对百分号原样保留
		{`%%`, `%%`}, // 空名原样保留
	}
	for _, c := range cases {
		if got := expandWindowsPercentVars(c.in, lookup); got != c.want {
			t.Errorf("expandWindowsPercentVars(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 空变量名不得查询 lookup：Windows 环境块含 "=C:=..." 这类空键隐藏项，
// 查询会被误命中并把 %% 展开成垃圾路径(评审发现 2026-10-10)。
func TestExpandWindowsPercentVarsEmptyNameSkipsLookup(t *testing.T) {
	calledWithEmpty := false
	lookup := func(name string) (string, bool) {
		if name == "" {
			calledWithEmpty = true
		}
		return "", false
	}
	if got := expandWindowsPercentVars(`%%x%%`, lookup); got != `%%x%%` {
		t.Errorf("空名应保留字面量，got %q", got)
	}
	if calledWithEmpty {
		t.Error("空变量名不应查询 lookup（会误命中 Windows 空键隐藏项）")
	}
}

// ---- windowsEnvVarLookup:Windows 语义下键大小写不敏感 ----

func TestWindowsEnvVarLookupCaseInsensitive(t *testing.T) {
	t.Setenv("APPDATA_TEST_ONLY", `C:\Users\u\Roaming`)
	lookup := windowsEnvVarLookup()
	if v, ok := lookup("appdata_test_only"); !ok || v != `C:\Users\u\Roaming` {
		t.Errorf("小写键应命中 APPDATA_TEST_ONLY，got %q ok=%v", v, ok)
	}
	if _, ok := lookup("DEFINITELY_NOT_SET_XYZ"); ok {
		t.Error("未设置变量不应命中")
	}
}

// ---- mergeWindowsUserPath:现条目在前保持原序,注册表新增条目追加在后 ----

func TestMergeWindowsUserPathAppendsNewEntries(t *testing.T) {
	got := mergeWindowsUserPath(`C:\Windows;C:\Windows\System32`, `C:\Users\u\AppData\Roaming\npm`)
	want := `C:\Windows;C:\Windows\System32;C:\Users\u\AppData\Roaming\npm`
	if got != want {
		t.Errorf("合并结果 %q，期望 %q", got, want)
	}
}

func TestMergeWindowsUserPathDedupCaseInsensitive(t *testing.T) {
	got := mergeWindowsUserPath(`C:\Foo;D:\Bar`, `c:\foo;D:\BAR\;e:\baz`)
	want := `C:\Foo;D:\Bar;e:\baz`
	if got != want {
		t.Errorf("大小写与尾分隔符应视为同一条目，got %q", got)
	}
}

func TestMergeWindowsUserPathSkipsEmptyEntries(t *testing.T) {
	got := mergeWindowsUserPath(`;;C:\Windows;;`, `;E:\Tools;;`)
	want := `C:\Windows;E:\Tools`
	if got != want {
		t.Errorf("空白项应跳过，got %q", got)
	}
}

func TestMergeWindowsUserPathEmptySides(t *testing.T) {
	if got := mergeWindowsUserPath("", `E:\Tools;F:\X`); got != `E:\Tools;F:\X` {
		t.Errorf("空当前 PATH 应只含用户条目，got %q", got)
	}
	if got := mergeWindowsUserPath(`C:\Windows`, ""); got != `C:\Windows` {
		t.Errorf("空用户 PATH 应保持原样，got %q", got)
	}
}

// ---- mergeUserPathIntoEnv:定位 PATH 键(大小写不敏感)合并,无则追加 ----

func TestMergeUserPathIntoEnvReplacesExistingPath(t *testing.T) {
	env := []string{"TERM=xterm-256color", "Path=C:\\Windows", "HOME=/home/u"}
	got := mergeUserPathIntoEnv(env, `C:\Users\u\AppData\Roaming\npm`)
	if len(got) != 3 {
		t.Fatalf("不应增删条目，got %v", got)
	}
	found := false
	for _, kv := range got {
		if strings.HasPrefix(kv, "Path=") {
			found = true
			if !strings.Contains(kv, `C:\Users\u\AppData\Roaming\npm`) || !strings.Contains(kv, `C:\Windows`) {
				t.Errorf("Path 应同时含原值与新增目录，got %q", kv)
			}
		}
	}
	if !found {
		t.Errorf("PATH 条目丢失，got %v", got)
	}
}

func TestMergeUserPathIntoEnvUppercaseKeyPreserved(t *testing.T) {
	env := []string{"PATH=C:\\Windows"}
	got := mergeUserPathIntoEnv(env, `E:\Tools`)
	if len(got) != 1 || !strings.HasPrefix(got[0], "PATH=") {
		t.Errorf("应保留原键名 PATH=，got %v", got)
	}
	if !strings.Contains(got[0], "E:\\Tools") {
		t.Errorf("合并值缺失新增目录，got %q", got[0])
	}
}

func TestMergeUserPathIntoEnvAppendsWhenMissing(t *testing.T) {
	env := []string{"TERM=xterm"}
	got := mergeUserPathIntoEnv(env, `E:\Tools`)
	if len(got) != 2 || got[1] != "Path=E:\\Tools" {
		t.Errorf("无 PATH 时应追加 Path=，got %v", got)
	}
}
