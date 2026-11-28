package panelwin

import (
	"os"
	"path/filepath"
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
