//go:build darwin || windows

package cmd

import (
	"fmt"
	"os"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/panelwin"
	"github.com/spf13/cobra"
)

// panelCmd — 托盘面板原生壳子命令(实现计划 M2 Task 13)。由托盘
// companion(Task 15 launcher)spawn,不作为用户入口(Hidden);进程即
// 面板本身:点击才拉起+关闭即退(2026-11-28 撤销方案 B「首次后常驻」)——
// 点面板外/失焦 exit(0)、托盘图标 close 请求(SIGUSR1/WM_APP+1)exit(0)、
// quit 消息 exit(60),退出码契约见 panelwin.go(0=正常关闭、60=quit,
// 其余=崩溃→launcher 重拉/兜底)。
//
// ⚠️ 依赖闭包纪律:本文件 darwin||windows 才编译,internal/panelwin 不得
// 进入 Linux 上 ./cmd/aliang 的依赖闭包(cmd/linux_closure_test.go
// TestLinuxDepsExcludeGUI 把守);Linux 形态在 panel_stub_non_gui.go。
var panelCmd = &cobra.Command{
	Use:   "panel",
	Short: "Show the tray panel native window",
	Long: `Show the tray panel as a native window shell (WebView2 / WKWebView).

This command is spawned by the tray companion; it is not intended for
interactive use.`,
	Hidden: true,
	RunE:   runPanel,
}

func init() {
	panelCmd.Flags().String("url", "", "panel page URL (loopback origin becomes the navigation whitelist)")
	panelCmd.Flags().Int("ax", 0, "tray icon rectangle X (global top-left origin; unit follows platform: macOS=points, Windows=physical pixels, same as TrayRect)")
	panelCmd.Flags().Int("ay", 0, "tray icon rectangle Y (global top-left origin; unit follows platform: macOS=points, Windows=physical pixels, same as TrayRect)")
	panelCmd.Flags().Int("w", 22, "tray icon rectangle width")
	panelCmd.Flags().Int("h", 22, "tray icon rectangle height")
	panelCmd.Flags().Int("pw", panelwin.DefaultPanelW, "panel logical width")
	panelCmd.Flags().Int("ph", panelwin.DefaultPanelH, "panel logical height")
	panelCmd.Flags().String("side", string(panelwin.SideBottom), "panel side relative to the tray icon: top|bottom")
	panelCmd.Flags().String("open-dashboard", "", "URL opened for open kind=dashboard")
	panelCmd.Flags().String("open-peer", "", "URL opened for open kind=peer")
	rootCmd.AddCommand(panelCmd)
}

func runPanel(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	pageURL, _ := flags.GetString("url")
	ax, _ := flags.GetInt("ax")
	ay, _ := flags.GetInt("ay")
	w, _ := flags.GetInt("w")
	h, _ := flags.GetInt("h")
	panelW, _ := flags.GetInt("pw")
	panelH, _ := flags.GetInt("ph")
	sideStr, _ := flags.GetString("side")
	openDashboard, _ := flags.GetString("open-dashboard")
	openPeer, _ := flags.GetString("open-peer")

	// 高度持久化(首帧尺寸修正 Part 2,2026-11-28 用户反馈「点击瞬间出现
	// 大空白区域再缩到目标尺寸」):上次会话 resize 落盘的稳态逻辑高度优先
	// 生效——launcher(app/tray/panel_bridge_m2.go)恒传缺省 560,--ph 恒
	// 处于显式传入态,持久值不优先则机制是死代码;无效(缺文件/损坏/越界)
	// 回落 flag 值(缺省 560)。窗口出生即正确尺寸,点击瞬间的空白闪变消失
	// (跨次内容高度变化的残余闪变由壳侧「首次上屏延迟到首次 resize 到位」
	// 兜住,panelwin_darwin.m / panelwin_windows.go)。
	//
	// 判定语义明示(P2-2):判据是 persisted != DefaultPanelH 才覆盖——
	// 持久值=560 与显式 --ph 700 同现时 flag 胜(持久值撞缺省值不可分辨,
	// 宁取显式;load 侧无效回落 DefaultPanelH 的形态同走此分支回落 flag 值)。
	// 接线形态由 cmd/panel_test.go TestPanelCmdPersistedHeightFirst 锚定。
	if persisted := panelwin.LoadLastPanelHeight(); persisted != panelwin.DefaultPanelH {
		panelH = persisted
	}

	// 导航白名单锚点校验:--url 必须是回环 origin(非回环/解析失败直接
	// 拒绝启动——面板等于无鉴权控制台,不得加载任何第三方内容,调研 §6.3
	// 不变量)。origin 本体由壳内从 opts.URL 重新提取(两端壳各自 derive,
	// 口径同 LoopbackOrigin),这里只做启动闸。
	if _, err := panelwin.LoopbackOrigin(pageURL); err != nil {
		return fmt.Errorf("--url: %w", err)
	}

	var side panelwin.Side
	switch sideStr {
	case string(panelwin.SideTop):
		side = panelwin.SideTop
	case string(panelwin.SideBottom):
		side = panelwin.SideBottom
	default:
		return fmt.Errorf("--side 必须为 top|bottom,得到 %q", sideStr)
	}

	x, y := panelwin.AnchorToWindowPos(ax, ay, w, h, side, panelW, panelH)

	opts := panelwin.Options{
		URL:           pageURL,
		X:             x,
		Y:             y,
		PanelW:        panelW,
		PanelH:        panelH,
		OpenDashboard: openDashboard,
		OpenPeer:      openPeer,
		Side:          side, // resize 高度变化的锚定边(Windows 壳 applyHeight 消费)
	}

	logger.Info(fmt.Sprintf("panel window: anchor=(%d,%d) panel=%dx%d side=%s", x, y, panelW, panelH, side))

	// 阻塞至面板退出;退出码契约(0/60/其余)由 launcher 消费。
	os.Exit(panelwin.NewHost().Run(opts))
	return nil
}
