package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// panelCmdSource 读取 cmd/panel.go 源码(结构锚测试用)。⚠️ 本测试只读文本、
// 不 import internal/panelwin——cmd/panel.go 是 darwin||windows 才编译的
// GUI 文件,Linux 上 import 它会撞 TestLinuxDepsExcludeGUI 的闭包门;
// 文本锚 GOOS 无关,三平台测试都跑(linux_closure_test.go 的门只看
// ./cmd/aliang 二进制的依赖闭包,测试文件不在闭包内,但零 import 最稳)。
func panelCmdSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "panel.go"))
	if err != nil {
		t.Fatalf("读取 cmd/panel.go: %v", err)
	}
	return string(src)
}

// TestPanelCmdPersistedHeightFirst —— 高度持久化接线锚(P2-2):cmd/panel.go
// 的「持久值优先于 --ph」此前仅手工冒烟覆盖,落为源码结构锚:
//
//  1. --ph 的 flag 缺省=panelwin.DefaultPanelH(launcher 恒传缺省 560,
//     --ph 恒处于显式传入态——持久值不优先则机制是死代码);
//  2. LoadLastPanelHeight 返回值优先于 flag 值:判据是
//     persisted != DefaultPanelH 才覆盖——语义(接线注释同款):持久值=560
//     与显式 --ph 700 同现时 flag 胜(持久值撞缺省值不可分辨,宁取显式);
//     无效(缺文件/损坏/越界)时 LoadLastPanelHeight 返回 DefaultPanelH,
//     判据不成立,回落 flag 值;
//  3. 覆盖发生在 flag 读取之后、Options 装配之前(出生尺寸=持久值的接线位)。
func TestPanelCmdPersistedHeightFirst(t *testing.T) {
	src := panelCmdSource(t)

	// ① flag 缺省锚:--ph 缺省=panelwin.DefaultPanelH(非字面量 560,
	//    缺省值单源)。
	if !strings.Contains(src, `panelCmd.Flags().Int("ph", panelwin.DefaultPanelH,`) {
		t.Fatal("cmd/panel.go 的 --ph flag 缺省不再是 panelwin.DefaultPanelH(缺省值单源回归)")
	}

	// ② 持久值优先锚:LoadLastPanelHeight 返回值≠缺省才覆盖 panelH
	//    (判定形态逐字锚——判据写错(如恒覆盖/恒不覆盖)即红)。
	wire := `if persisted := panelwin.LoadLastPanelHeight(); persisted != panelwin.DefaultPanelH {`
	wireIdx := strings.Index(src, wire)
	if wireIdx < 0 {
		t.Fatalf("cmd/panel.go 缺「持久值优先」接线(形态=%q)——LoadLastPanelHeight 返回值未接回出生尺寸,高度持久化机制死代码:\n%s", wire, src)
	}

	// ③ 覆盖语句在接线块内、且在 flag 读取之后。
	assignIdx := strings.Index(src[wireIdx:], "panelH = persisted")
	if assignIdx < 0 {
		t.Fatal("cmd/panel.go 持久值接线块缺 panelH = persisted(读了不用=死代码)")
	}
	flagReadIdx := strings.Index(src, `flags.GetInt("ph")`)
	if flagReadIdx < 0 {
		t.Fatal("cmd/panel.go 缺 --ph flag 读取(flags.GetInt)")
	}
	if flagReadIdx > wireIdx {
		t.Fatalf("--ph flag 读取(%d)必须在持久值接线(%d)之前(覆盖的是 flag 值,读反了覆盖无效)", flagReadIdx, wireIdx)
	}

	// ④ 接线语义注释在册:persisted != DefaultPanelH 判定的取舍(持久值
	//    撞缺省 560 时显式 --ph 胜)必须有明示,防后人把判据「修」成恒覆盖。
	if !strings.Contains(src, "persisted != DefaultPanelH") || !strings.Contains(src, "flag 胜") {
		t.Fatal("cmd/panel.go 持久值接线缺语义注释(持久值=560 与显式 --ph 同现时 flag 胜的取舍未明示)")
	}
}
