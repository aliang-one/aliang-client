package panelwin

import "testing"

// resizeWindowRect 表驱动(评审增益批必修 3,先例同 panelwin_reanchor_test.go):
// 高度自适应的几何解算抽纯函数钉死,GOOS 无关零窗口零 API。
//
// 回归锚:「SideBottom 超屏钳」用例钉的是评审必修 2 的旧公式缺陷——旧实现
// 在钳制分支写 newH += curY-8(基准取错变量,应为钳后的 y=8),评审实算
// curY=400/curH=500/请求 1500 时旧公式给 y=8/h=1892,底边冲到 1900 越出
// 屏幕;正确解 h=底边-8=892,底边保持 900。该用例对旧公式必红(变异验证
// 已跑,红→绿证据见提交说明);其余钳制分支同理由各用例钉死。
func TestResizeWindowRect(t *testing.T) {
	cases := []struct {
		name                       string
		curX, curY, curW, curH     int
		logicalH                   float64
		scale                      float64
		side                       Side
		workH                      int
		wantX, wantY, wantW, wantH int
	}{
		{
			name: "SideTop 正常:顶边固定向下生长",
			curX: 10, curY: 100, curW: 340, curH: 500,
			logicalH: 700, scale: 1.0, side: SideTop, workH: 1392,
			wantX: 10, wantY: 100, wantW: 340, wantH: 700,
		},
		{
			name: "SideTop workH 钳:底缘留 8",
			curX: 10, curY: 100, curW: 340, curH: 500,
			logicalH: 2000, scale: 1.0, side: SideTop, workH: 1392,
			// y+newH ≤ workH-8 → newH = 1384-100 = 1284
			wantX: 10, wantY: 100, wantW: 340, wantH: 1284,
		},
		{
			name: "SideTop workH=0:采样失败不钳",
			curX: 10, curY: 100, curW: 340, curH: 500,
			logicalH: 2000, scale: 1.0, side: SideTop, workH: 0,
			wantX: 10, wantY: 100, wantW: 340, wantH: 2000,
		},
		{
			name: "SideTop 钳后非正:面板已在工作区外,整窗不动",
			curX: 10, curY: 1390, curW: 340, curH: 100,
			logicalH: 700, scale: 1.0, side: SideTop, workH: 1392,
			// workH-8-curY = 1384-1390 = -6 ≤ 0 → 保持原矩形
			wantX: 10, wantY: 1390, wantW: 340, wantH: 100,
		},
		{
			name: "SideTop 钳进 min 之下:屏约束优先于最低可用高",
			curX: 10, curY: 1300, curW: 340, curH: 90,
			logicalH: 700, scale: 1.0, side: SideTop, workH: 1392,
			// newH = 1384-1300 = 84(低于 min 240,屏约束赢)
			wantX: 10, wantY: 1300, wantW: 340, wantH: 84,
		},
		{
			name: "SideBottom 正常:底边固定向上生长",
			curX: 10, curY: 400, curW: 340, curH: 500,
			logicalH: 700, scale: 1.0, side: SideBottom, workH: 1392,
			// 底边 900 不动:y = 900-700 = 200
			wantX: 10, wantY: 200, wantW: 340, wantH: 700,
		},
		{
			name: "SideBottom 超屏钳(评审实算用例):底边保持 900",
			curX: 10, curY: 400, curW: 340, curH: 500,
			logicalH: 1500, scale: 1.0, side: SideBottom, workH: 1392,
			// y=900-1500=-600 < 8 → y=8,h=900-8=892(旧公式 1892 必红)
			wantX: 10, wantY: 8, wantW: 340, wantH: 892,
		},
		{
			name: "SideBottom 连续 resize 幂等:以钳后矩形为基准不漂移",
			curX: 10, curY: 8, curW: 340, curH: 892,
			logicalH: 892, scale: 1.0, side: SideBottom, workH: 1392,
			// 底边 900 不动:y = 900-892 = 8,恰好落在间隙上不触发钳
			wantX: 10, wantY: 8, wantW: 340, wantH: 892,
		},
		{
			name: "min 钳:240 最低逻辑可用高",
			curX: 10, curY: 400, curW: 340, curH: 500,
			logicalH: 100, scale: 1.0, side: SideBottom, workH: 1392,
			// newH=240,y = 900-240 = 660
			wantX: 10, wantY: 660, wantW: 340, wantH: 240,
		},
		{
			name: "scale 1.25 正常换算",
			curX: 10, curY: 400, curW: 340, curH: 625,
			logicalH: 800, scale: 1.25, side: SideBottom, workH: 1740,
			// 物理高=1000,底边 1025 不动:y = 1025-1000 = 25
			wantX: 10, wantY: 25, wantW: 340, wantH: 1000,
		},
		{
			name: "min 钳×scale:240×1.25=300",
			curX: 10, curY: 400, curW: 340, curH: 625,
			logicalH: 100, scale: 1.25, side: SideBottom, workH: 1740,
			// newH=125 → min 300,y = 1025-300 = 725
			wantX: 10, wantY: 725, wantW: 340, wantH: 300,
		},
		{
			name: "新 h≤0(logicalH=0):整窗不动",
			curX: 10, curY: 400, curW: 340, curH: 500,
			logicalH: 0, scale: 1.0, side: SideBottom, workH: 1392,
			wantX: 10, wantY: 400, wantW: 340, wantH: 500,
		},
		{
			name: "新 h≤0(负值):整窗不动",
			curX: 10, curY: 400, curW: 340, curH: 500,
			logicalH: -50, scale: 1.0, side: SideTop, workH: 1392,
			wantX: 10, wantY: 400, wantW: 340, wantH: 500,
		},
		{
			name: "SideBottom 底边不高于间隙:钳后非正,整窗不动",
			curX: 10, curY: 0, curW: 340, curH: 8,
			logicalH: 700, scale: 1.0, side: SideBottom, workH: 1392,
			// 底边 8:钳 y=8 后 h=8-8=0 ≤ 0 → 保持原矩形
			wantX: 10, wantY: 0, wantW: 340, wantH: 8,
		},
		{
			name: "未知 side 兜底按 bottom(与 AnchorToWindowPos 同口径)",
			curX: 10, curY: 400, curW: 340, curH: 500,
			logicalH: 700, scale: 1.0, side: Side(""), workH: 1392,
			wantX: 10, wantY: 200, wantW: 340, wantH: 700,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gx, gy, gw, gh := resizeWindowRect(
				tc.curX, tc.curY, tc.curW, tc.curH,
				tc.logicalH, tc.scale, tc.side, tc.workH,
			)
			if gx != tc.wantX || gy != tc.wantY || gw != tc.wantW || gh != tc.wantH {
				t.Fatalf("resizeWindowRect(%d,%d,%d,%d, %v, %v, %s, %d) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					tc.curX, tc.curY, tc.curW, tc.curH,
					tc.logicalH, tc.scale, tc.side, tc.workH,
					gx, gy, gw, gh,
					tc.wantX, tc.wantY, tc.wantW, tc.wantH)
			}
		})
	}
}
