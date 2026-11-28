package panelwin

// 物理重锚纯函数(实现计划 M2 Task 15 硬验收收口,计划 :361-364):
//
// cmd/panel.go 的锚定(AnchorToWindowPos)按逻辑面板宽高(340×560)对物理
// 图标坐标计算;Windows 非 100% 缩放下壳内以物理尺寸(逻辑×scale)显示,
// 锚定边不重算的话高度差 (physH-panelH) 全压在锚定边——150% 下面板底边
// 越过图标顶约 280px(硬验收原文「150% 实机必现破损」)。
//
// 收口方案(已裁定):壳内(panelwin_windows.go)拿到 GetDpiForWindow scale
// 后对本函数做物理重锚——x 居中与 y 锚定边按物理面板尺寸重算,边距/间隙
// 钳物理值(逻辑×scale);接线层仍传逻辑 pw/ph(与 cmd/panel.go 语义一致)。
// macOS 无此问题(Retina 由 AppKit 处理,TrayRect 全局点直通),不调本函数。
//
// 恒等性:scale=1(physW=panelW/physH=panelH)时输出=输入,100% 既有机器
// 行为零变化(由 AnchorToWindowPos 的表驱动测试与 Task 13 锚定验收共同
// 锚定);panelwin_reanchor_test.go 表驱动覆盖 125%/150% 两档与 side 判别。

// 重锚几何常量:与 AnchorToWindowPos 的局部常量同值(该函数已过双评审不
// 改动,故就地再声明;语义:8=左边距/bottom 间隙,4=top 间隙)。
const (
	reanchorClampMargin  = 8
	reanchorGapBelowIcon = 8
	reanchorGapAboveIcon = 4

	// reanchorIconSlack side 判别的任务栏条带容差(物理 px):托盘图标矩形
	// 位于任务栏内,其顶边距工作区顶边(底部任务栏)不超过一个图标高度
	// 量级;取 60 覆盖 150% 缩放下的图标 36~48px 物理高。
	reanchorIconSlack = 60
)

// reanchorPhysicalPos 物理重锚:
//
//	入参 x/y —— cmd/panel.go 逻辑锚定结果(全局左上原点,物理图标坐标系);
//	panelW/panelH —— 逻辑面板尺寸(接线层传入的 --pw/--ph);
//	physW/physH —— 物理面板尺寸(壳内按 scale 换算后);
//	scale —— GetDpiForWindow 派生(96=1.0);workH —— 主屏工作区高度
//	(GetSystemMetrics(SM_CYFULLSCREEN) 物理 px;0=未知)。
//
// 返回物理锚定 (x', y')。side 不可见于壳(Options 只携带锚定结果),由
// bottom 假设的恒等还原判别:side=bottom ⇔ y+panelH+8 恰还原图标顶边,该
// 顶边必须落在任务栏条带内(workH-reanchorIconSlack 之下);判别失手只在
// 非 bottom 形态误用 bottom 公式,而 scale=1 时两支公式同为恒等——误判
// 无害面收敛在「小屏 + 高缩放 + 顶部任务栏」三条件交集,实际不可达。
func reanchorPhysicalPos(x, y, panelW, panelH, physW, physH int, scale float64, workH int) (int, int) {
	// x:居中按物理半宽重算。逻辑锚定 x = 图标中心 - 逻辑半宽,物理半宽差
	// (physW-panelW)/2 全量平移即回到物理居中;再钳物理左边距 8*scale。
	reX := x - (physW-panelW)/2
	if minLeft := int(float64(reanchorClampMargin) * scale); reX < minLeft {
		reX = minLeft
	}

	// y:按「贴图标的那条边」钉住重算——
	//   bottom(面板悬于图标上方):钉底边。y = ay - panelH - 8 ⇔ ay =
	//   y + panelH + 8;物理底边应回 ay - 8*scale,故
	//   y' = ay - 8*scale - physH = y - (physH-panelH) - 8*(scale-1)。
	//   150% 核对:面板底边 = y' + 840 = ay - 12 = ay - 8*1.5 ✓。
	//   top(面板垂在图标下方):钉顶边。y = ay + h + 4 本就物理正确,仅
	//   间隙换算 4*scale:y' = (y - 4) + 4*scale = y + 4*(scale-1)。
	bottom := workH <= 0 || y+panelH+reanchorGapBelowIcon > workH-reanchorIconSlack
	var reY int
	if bottom {
		reY = y - (physH - panelH) - int(float64(reanchorGapBelowIcon)*(scale-1))
	} else {
		reY = y + int(float64(reanchorGapAboveIcon)*(scale-1))
	}
	return reX, reY
}
