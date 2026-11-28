package panelwin

// panelwin_resize.go — 高度自适应的窗口矩形解算(评审增益批必修 3):把
// applyHeight(panelwin_windows.go)的几何计算抽成纯函数表驱动钉死,先例同
// panelwin_reanchor.go——公式错误在纯函数测试上必红,不依赖窗口/系统 API,
// GOOS 无关可跑(macOS/Linux runner 均可验证 Windows 壳几何)。

const (
	// resizeMinLogicalH —— 高度自适应的最低逻辑可用高(解算层兜底下限):
	// 受理闸(壳侧 MessageCallback 的 ≥280,评审 Minor-7)与 macOS clamp
	// (panelwin_darwin.m 的 newH < 280)都取 280,本值 240 是几何层最后一道
	// 兜底——闸漏掉的退化值到解算层仍钳不出「一条线」的窗(双层防御)。
	resizeMinLogicalH = 240

	// resizeScreenGap —— 钳屏时与屏幕边缘留的物理 px:SideTop 的底缘与
	// SideBottom 的顶缘各留一份(面板不没入屏幕外)。与
	// reanchorGapBelowIcon 同值但语义不同(那边是锚定间隙),就地声明;
	// 评审变异验证要能只动这一处。
	resizeScreenGap = 8
)

// resizeWindowRect — 高度自适应的窗口矩形解算(纯函数):给定当前物理矩形/
// 新逻辑高/scale/锚定边/工作区高,返回新矩形(物理 px)。
//
// 规则(评审定稿):
//   - 物理高 = logicalH×scale,先过 min=resizeMinLogicalH×scale 兜底;
//   - SideTop:顶边固定向下生长(y 不变);workH>0(主屏工作区物理高已知,
//     0=采样失败/未知则不钳)时钳 y+h≤workH-resizeScreenGap,钳后 ≤0
//     (面板已在工作区外)保持原矩形;
//   - default(SideBottom 与未知值兜底,与 AnchorToWindowPos 同口径):
//     底边固定向上生长(y+h 不变),y=底边-新高;过度上生长挤过屏顶时
//     y 钳 resizeScreenGap、高度回补为底边-resizeScreenGap(保住顶边可见);
//   - 换算不出正物理高(logicalH≤0)或钳后高 ≤0:整窗不动(返回原矩形)。
//
// x/w 恒直通(resize 协议只带高度;宽度自适应不在范围)。
func resizeWindowRect(curX, curY, curW, curH int, logicalH float64, scale float64, side Side, workH int) (x, y, w, h int) {
	newH := int(logicalH * scale)
	if newH <= 0 {
		return curX, curY, curW, curH // 换算不出正物理高:整窗不动
	}
	if minH := int(float64(resizeMinLogicalH) * scale); newH < minH {
		newH = minH
	}
	switch side {
	case SideTop:
		// 顶边固定,向下生长:y 不变;超出主屏工作区(workH=SM_CYFULLSCREEN
		// 物理高)时钳高,底缘留一份屏幕间隙。
		if workH > 0 && curY+newH > workH-resizeScreenGap {
			newH = workH - resizeScreenGap - curY
		}
		if newH <= 0 {
			return curX, curY, curW, curH // 面板已在工作区外:整窗不动
		}
		return curX, curY, curW, newH
	default:
		// 底边固定,向上生长:锚定底边 y+高 不动,y = 底边-新物理高。
		// 过度上生长挤过屏顶时钳:y 钳屏幕间隙,高度回补为底边-间隙。
		bottom := curY + curH
		newY := bottom - newH
		if newY < resizeScreenGap {
			newY = resizeScreenGap
			newH = bottom - resizeScreenGap
		}
		if newH <= 0 {
			return curX, curY, curW, curH // 底边不高于间隙:钳不出正高,整窗不动
		}
		return curX, newY, curW, newH
	}
}
