package panelwin

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// 高度持久化(首帧尺寸修正 Part 2,2026-11-28 用户反馈「点击瞬间大空白
// 区域再缩到目标尺寸」)的状态文件写读往返表驱动:
//
//   - 落盘:两侧壳在 resize 分派时把稳态逻辑高度写 ~/.aliang/panel/last-height
//     (macOS 经 cgo 导出 panelwinGoHeightChanged,Windows 直调);越界值不落盘
//     (读回侧同样拒收,写了也白写),目录不存在 MkdirAll 兜建,读写失败静默
//     降级不阻断;
//   - 读回:cmd/panel.go 启动时读取,存在且 240≤h≤2000 才生效,否则(缺文件/
//     损坏/越界)回落缺省 DefaultPanelH=560——窗口出生即正确尺寸,首帧不再
//     从 560 缩到实际高度。
//
// 测试经 panelHeightStatePath 注入口覆写到临时目录,不碰真实家目录。

// withTempPanelHeightState 把状态文件路径注入口覆写到 t 临时目录并注册还原。
func withTempPanelHeightState(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".aliang", "panel", "last-height")
	orig := panelHeightStatePath
	panelHeightStatePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { panelHeightStatePath = orig })
	return path
}

func TestPanelHeightStateRoundTrip(t *testing.T) {
	path := withTempPanelHeightState(t)

	// 缺文件:回落缺省 560(不阻断启动)。
	if got := LoadLastPanelHeight(); got != DefaultPanelH {
		t.Fatalf("缺文件时 LoadLastPanelHeight = %d, want 缺省 %d", got, DefaultPanelH)
	}

	// 落盘 → 目录自建 → 文件为纯数字文本 → 读回一致。
	persistLastPanelHeight(432)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("落盘后读不到状态文件: %v", err)
	}
	if got := string(raw); got != "432" {
		t.Fatalf("状态文件内容 = %q, want \"432\"(纯数字逻辑高度文本)", got)
	}
	if got := LoadLastPanelHeight(); got != 432 {
		t.Fatalf("落盘 432 后 LoadLastPanelHeight = %d, want 432", got)
	}

	// 覆写生效(同会话内多次 resize 持续更新)。
	persistLastPanelHeight(388)
	if got := LoadLastPanelHeight(); got != 388 {
		t.Fatalf("覆写 388 后 LoadLastPanelHeight = %d, want 388", got)
	}
}

func TestPanelHeightStateRangeGuard(t *testing.T) {
	path := withTempPanelHeightState(t)

	// 越界值不落盘:低于 240 / 高于 2000 的脏值直接拒收(读回侧同窗拒收,
	// 写了也白写)。
	persistLastPanelHeight(100)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("越界低值 100 不应落盘(文件不该存在): err=%v", err)
	}
	persistLastPanelHeight(2500)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("越界高值 2500 不应落盘(文件不该存在): err=%v", err)
	}

	// 有效窗边界:240/2000 收,239/2001 拒(拒=不落盘:先清掉上一份有效
	// 文件再写越界值,文件应保持不存在,读回回落缺省)。
	persistLastPanelHeight(240)
	if got := LoadLastPanelHeight(); got != 240 {
		t.Fatalf("下边界 240 应有效,得到 %d", got)
	}
	persistLastPanelHeight(2000)
	if got := LoadLastPanelHeight(); got != 2000 {
		t.Fatalf("上边界 2000 应有效,得到 %d", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("清理状态文件: %v", err)
	}
	persistLastPanelHeight(239)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("越界低值 239 不应落盘: err=%v", err)
	}
	if got := LoadLastPanelHeight(); got != DefaultPanelH {
		t.Fatalf("239 低于有效窗下限,应回落缺省 %d,得到 %d", DefaultPanelH, got)
	}
	persistLastPanelHeight(2001)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("越界高值 2001 不应落盘: err=%v", err)
	}
	if got := LoadLastPanelHeight(); got != DefaultPanelH {
		t.Fatalf("2001 高于有效窗上限,应回落缺省 %d,得到 %d", DefaultPanelH, got)
	}
}

func TestPanelHeightStateCorruptedFallsBack(t *testing.T) {
	path := withTempPanelHeightState(t)

	cases := []struct {
		name    string
		content string
	}{
		{name: "纯垃圾", content: "abc"},
		{name: "空文件", content: ""},
		{name: "数字带尾巴", content: "432 abc"},
		{name: "负数", content: "-5"},
		{name: "带引号的 JSON", content: `"432"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 预置损坏内容:目录先兜建(生产形态目录由 persist 的 MkdirAll
			// 兜建,这里测试直写文件需自备目录)。
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("兜建临时目录: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("预置损坏状态文件: %v", err)
			}
			if got := LoadLastPanelHeight(); got != DefaultPanelH {
				t.Fatalf("损坏内容 %q 应回落缺省 %d,得到 %d", tc.content, DefaultPanelH, got)
			}
		})
	}
}

// TestPanelHeightStateAtomicWrite —— 状态文件写原子化(P3 顺手):temp+rename
// 防「读回侧读到半截数字」的撕裂窗口——读回(LoadLastPanelHeight)发生在
// 下次面板启动的另一进程,直接 WriteFile 本体的写入窗口内若被读到,拿到的
// 是截断文本(atoi 失败→回落缺省=持久化静默失效)。两面锚:
//  1. 行为面:落盘后状态目录内无 .tmp 临时名残留(temp+rename 后临时名消失);
//  2. 形态面:persistLastPanelHeight 必须写同目录临时名再 os.Rename,不得
//     直接 os.WriteFile 到状态文件本体。
func TestPanelHeightStateAtomicWrite(t *testing.T) {
	path := withTempPanelHeightState(t)

	persistLastPanelHeight(517)
	if got := LoadLastPanelHeight(); got != 517 {
		t.Fatalf("原子写落盘 517 后 LoadLastPanelHeight = %d, want 517", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("读状态目录: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("状态目录残留临时文件 %q(temp+rename 后不应留存)", e.Name())
		}
	}

	// 形态面锚:persistLastPanelHeight 函数体内必须出现 os.Rename,且不得
	// 直接 WriteFile 到状态文件本体(path)。
	src := panelwinSource(t)
	fnIdx := strings.Index(src, "func persistLastPanelHeight(")
	if fnIdx < 0 {
		t.Fatal("panelwin.go 缺 persistLastPanelHeight")
	}
	body := src[fnIdx:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "os.Rename(") {
		t.Fatalf("persistLastPanelHeight 缺 os.Rename(直接写状态文件本体=撕裂读窗口):\n%s", body)
	}
	if regexp.MustCompile(`os\.WriteFile\(\s*path,`).MatchString(body) {
		t.Fatalf("persistLastPanelHeight 直接 os.WriteFile 到状态文件本体(撕裂读窗口,应写同目录临时名后 rename):\n%s", body)
	}
}

// panelwinSource 读取 panelwin.go 源码(形态锚测试用)。
func panelwinSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "panelwin.go"))
	if err != nil {
		t.Fatalf("读取 panelwin.go: %v", err)
	}
	return string(src)
}
