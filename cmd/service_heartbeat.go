// 服务存活心跳:Windows 服务包装每 interval 往 path touch 一次时间戳。
// 用途是死后取证——7034 静默死亡(2026-11-19 0.196 五连)既无 panic 也无
// 收尾日志,唯一可用的现场线索是「死时心跳新鲜度」:
//   - 心跳断点≈死亡时刻且死前仍新鲜 = 进程被外部强杀(SCM 判无响应
//     TerminateProcess,退出码 1067)——进程本体直到最后一刻是活的;
//   - 心跳早已停摆 = 进程先饿死再被收尸,断点与死亡时刻的差值即饿死窗口。
//
// ctx 取消即停写,文件停留在最后一次心跳,不删不归零(删除反而抹掉证据)。
//
// 取证协议:启动时若已存在旧心跳文件(= 上个进程的死亡现场),先保档为
// <path>.prev 再开写——7034 后的常态正是「杀了马上拉起」,不保档则每次
// 重启都会把上一份死亡时刻证据覆掉。
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// serviceHeartbeatInterval 是心跳节拍:远小于 SCM 判死阈值
// (ServicesPipeTimeout 默认 30s),保证「死前仍新鲜」可判。
const serviceHeartbeatInterval = 10 * time.Second

// runHeartbeat 阻塞式心跳循环:建目录 → 保档旧现场 → 立即写一次,此后每
// interval 一跳,ctx 取消即返回。path 为空或 interval 非正时空转返回
// (防御,不给调用方添错路径)。
func runHeartbeat(ctx context.Context, path string, interval time.Duration) {
	if path == "" || interval <= 0 {
		return
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			// 目录建不出(权限等)时后续每次写都会失败,提前止损不再空转。
			logger.Debug(fmt.Sprintf("heartbeat mkdir failed: %v", err))
			return
		}
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Rename(path, path+".prev") // 保档旧进程终态;失败则放弃保档不阻塞
	}
	writeHeartbeat(path)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			writeHeartbeat(path)
		}
	}
}

// writeHeartbeat 单次写心跳(UnixNano 文本)。心跳是取证辅助,写失败只留
// 调试痕不干预运行——不能让取证机制反过来杀死进程。
func writeHeartbeat(path string) {
	content := fmt.Sprintf("%d\n", time.Now().UnixNano())
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		logger.Debug(fmt.Sprintf("heartbeat write failed: %v", err))
	}
}
