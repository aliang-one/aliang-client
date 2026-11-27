//go:build !darwin && !windows

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// panelCmd — 面板壳的 Linux stub(照 tray_stub_non_gui.go 先例:报错退出)。
// Linux 不做面板(Wayland 无法贴图标弹出,调研结论);真正实现见 panel.go
// (darwin||windows,internal/panelwin 壳)。build tag 保证 internal/panelwin
// 及其 WebView2 依赖不进入 Linux 的 ./cmd/aliang 依赖闭包
// (cmd/linux_closure_test.go TestLinuxDepsExcludeGUI 把守)。
var panelCmd = &cobra.Command{
	Use:   "panel",
	Short: "Show the tray panel native window",
	Long: `Show the tray panel as a native window shell (WebView2 / WKWebView).

This build does not include the panel window shell on this platform.`,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("panel mode is not available in this Linux build; open the dashboard in a browser instead")
	},
}

func init() {
	panelCmd.Flags().String("url", "", "panel page URL")
	panelCmd.Flags().Int("ax", 0, "tray icon rectangle X")
	panelCmd.Flags().Int("ay", 0, "tray icon rectangle Y")
	panelCmd.Flags().Int("w", 0, "tray icon rectangle width")
	panelCmd.Flags().Int("h", 0, "tray icon rectangle height")
	panelCmd.Flags().Int("pw", 0, "panel logical width")
	panelCmd.Flags().Int("ph", 0, "panel logical height")
	panelCmd.Flags().String("side", "", "panel side relative to the tray icon")
	panelCmd.Flags().String("open-dashboard", "", "URL opened for open kind=dashboard")
	panelCmd.Flags().String("open-peer", "", "URL opened for open kind=peer")
	rootCmd.AddCommand(panelCmd)
}
