package panelwin

import "testing"

// 物理重锚纯函数测试(M2 Task 15 硬验收收口,计划 :361-364):锚定坐标
// (cmd/panel.go 按逻辑面板宽高算出)在 150% 缩放下底边越过图标顶约 280px,
// 重锚把 y 与 x 居中按物理面板尺寸重算并钳物理边距。表驱动,零窗口零 API。
//
// 锚定还原恒等式(AnchorToWindowPos 逆运算,自检基准):
//	side=bottom → 图标顶边 ay = y + panelH + 8;面板底边 = y + panelH
//	side=top    → 图标底边 ay + h = y - 4;面板顶边 = y

const (
	anchorTestW = 340
	anchorTestH = 560
)

func TestReanchorPhysicalPosIdentityAtScale100(t *testing.T) {
	// scale=1:物理尺寸=逻辑尺寸,间隙×(scale-1)=0——重锚必须逐值恒等
	// (否则 100% 缩放的既有机器会被无谓挪动,回归 Task 13 锚定表)。
	for _, tc := range []struct {
		name string
		x, y int
	}{
		{"bottom形态", 900, 832}, // ay=1400:y=1400-560-8
		{"top形态", 1200, 44},    // ay=36,h=24:y=36+24+4
		{"x左钳边距形态", 8, 832},    // ax 靠左被钳到 8
	} {
		t.Run(tc.name, func(t *testing.T) {
			gx, gy := reanchorPhysicalPos(tc.x, tc.y, anchorTestW, anchorTestH, anchorTestW, anchorTestH, 1.0, 1392)
			if gx != tc.x || gy != tc.y {
				t.Fatalf("scale=1 重锚不恒等:(%d,%d)→(%d,%d), want 恒等", tc.x, tc.y, gx, gy)
			}
		})
	}
}

func TestReanchorPhysicalPosBottomSide150(t *testing.T) {
	// 150% 破损形态(计划硬验收原文):底部任务栏,图标顶边 ay=1686(物理)。
	// 逻辑锚定 y = 1686 - 560 - 8 = 1118;旧形态直接把物理高 840 的面板顶在
	// 1118 → 底边 1958,越过图标顶 272px(≈计划说的 280)。重锚后底边必须
	// 回到 ay - 8*scale = 1686 - 12 = 1674。
	const (
		ay        = 1686 // 图标顶边(物理 px)
		logicalY  = ay - anchorTestH - 8
		physW150  = anchorTestW * 3 / 2 // 510
		physH150  = anchorTestH * 3 / 2 // 840
		scale150  = 1.5
		workAreaH = 1728 // 1800 屏高 - 72 任务栏
	)
	gx, gy := reanchorPhysicalPos(1200, logicalY, anchorTestW, anchorTestH, physW150, physH150, scale150, workAreaH)

	// y:面板底边钉在 ay-8*scale=1674,顶边 y' = 底边 - 物理高 = 834
	// (= 逻辑 y - (physH-panelH) - 8*(scale-1) = 1118-280-4)。
	if wantY := ay - 12 - physH150; gy != wantY {
		t.Fatalf("150%% bottom 重锚 y = %d, want %d(底边回钉 ay-12 后取顶边)", gy, wantY)
	}
	// x:居中按物理半宽重算(x = 图标中心 - 物理半宽,1200 是逻辑 x = 图标中心-170
	// 的形态,图标中心=1370)
	if wantX := 1200 - (physW150-anchorTestW)/2; gx != wantX {
		t.Fatalf("150%% bottom 重锚 x = %d, want %d(居中按物理半宽)", gx, wantX)
	}
}

func TestReanchorPhysicalPosTopSide150(t *testing.T) {
	// 150% 顶部任务栏:图标底边 ay+h=78(ay=36,h=42 物理图标高)。逻辑锚定
	// y = 78 + 4 = 82——顶边本来就已物理正确,重锚只需把顶边间隙换算为
	// 4*scale = 6:y' = 82 + 4*(1.5-1) = 84。
	const (
		logicalY = 82
		physW150 = anchorTestW * 3 / 2
		physH150 = anchorTestH * 3 / 2
	)
	gx, gy := reanchorPhysicalPos(1200, logicalY, anchorTestW, anchorTestH, physW150, physH150, 1.5, 1728)
	if wantY := logicalY + 2; gy != wantY {
		t.Fatalf("150%% top 重锚 y = %d, want %d(顶边间隙换算 4*scale)", gy, wantY)
	}
	if wantX := 1200 - (physW150-anchorTestW)/2; gx != wantX {
		t.Fatalf("150%% top 重锚 x = %d, want %d", gx, wantX)
	}
}

func TestReanchorPhysicalPosSideDiscrimination(t *testing.T) {
	// side 判别基准:bottom 假设可恒等还原图标顶边(ay=y+panelH+8),该顶边
	// 必须落在任务栏条带内(workH-图标容差)才判 bottom;top 形态还原出的
	// 「图标顶边」远离条带 → 判 top。表:
	//   bottom@150%:ay=1686 → 还原 1686 > 1728-60 → bottom
	//   top@150%:y=88(ay=36,h=42)→ 还原 88+568=656 ≤ 1668 → top
	//   workH 未知(0):默认按 bottom 收口(破损形态在底部任务栏)
	const physW150 = anchorTestW * 3 / 2
	const physH150 = anchorTestH * 3 / 2
	t.Run("底部任务栏判bottom", func(t *testing.T) {
		// 顶边期望:底边回钉 ay-12=1674 → 顶边=1674-840=834。
		_, gy := reanchorPhysicalPos(1200, 1118, anchorTestW, anchorTestH, physW150, physH150, 1.5, 1728)
		if gy != 1674-physH150 {
			t.Fatalf("未按 bottom 重锚:y=%d, want %d", gy, 1674-physH150)
		}
	})
	t.Run("顶部任务栏判top", func(t *testing.T) {
		_, gy := reanchorPhysicalPos(1200, 88, anchorTestW, anchorTestH, physW150, physH150, 1.5, 1728)
		if gy != 88+2 {
			t.Fatalf("未按 top 重锚:y=%d", gy)
		}
	})
	t.Run("工作区未知默认bottom", func(t *testing.T) {
		_, gy := reanchorPhysicalPos(1200, 1118, anchorTestW, anchorTestH, physW150, physH150, 1.5, 0)
		if gy != 1674-physH150 {
			t.Fatalf("workH=0 未按 bottom 兜底:y=%d, want %d", gy, 1674-physH150)
		}
	})
}

func TestReanchorPhysicalPosXClampedToPhysicalMargin(t *testing.T) {
	// 图标贴屏幕左缘:逻辑 x 已被钳到 8,150% 下按物理半宽平移会出负值,
	// 必须钳到物理边距 8*scale = 12。
	const physW150 = anchorTestW * 3 / 2
	const physH150 = anchorTestH * 3 / 2
	gx, _ := reanchorPhysicalPos(8, 1118, anchorTestW, anchorTestH, physW150, physH150, 1.5, 1728)
	if wantX := 12; gx != wantX {
		t.Fatalf("150%% 左钳物理边距 x = %d, want %d", gx, wantX)
	}
}

func TestReanchorPhysicalPos125(t *testing.T) {
	// 125%(Task 16 实机走查项):图标顶 ay=1400(1440 屏-40 任务栏),
	// 逻辑 y=832;physH=700 → 底边应回钉 ay-8*1.25=1390,顶边
	// y' = 1390 - 700 = 690(= 832-(700-560)-int(8*0.25)=832-140-2)。
	const (
		ay       = 1400
		logicalY = ay - anchorTestH - 8
		physW125 = anchorTestW * 5 / 4 // 425
		physH125 = anchorTestH * 5 / 4 // 700
	)
	gx, gy := reanchorPhysicalPos(1000, logicalY, anchorTestW, anchorTestH, physW125, physH125, 1.25, 1400)
	if wantY := ay - 10 - physH125; gy != wantY {
		t.Fatalf("125%% bottom 重锚 y = %d, want %d(底边回钉 ay-10 后取顶边)", gy, wantY)
	}
	if wantX := 1000 - (physW125-anchorTestW)/2; gx != wantX {
		t.Fatalf("125%% 重锚 x = %d, want %d", gx, wantX)
	}
}
