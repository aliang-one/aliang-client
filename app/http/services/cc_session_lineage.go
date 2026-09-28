package services

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentAILineageNotice 是谱系标记条目的正文：面向人的同步说明。
const agentAILineageNotice = "同步通知/Sync notice: 手机端发来的新消息已由 AI 回复完毕,本轮对话已并入上方历史记录。/ A phone turn completed and its exchange is merged into the history above."

// 会话谱系维护：CC 的 resume 只渲染"最新 cli(entrypoint) 链"的祖先——手机回合
// 由 agent 无头 claude(sdk-cli) 写入，挂在交互链尖下做兄弟分支，TUI 关闭期间
// 的回合在退出再打开后永久不可见（2026-09-26 b1f82c61 / 2026-09-28 3d06bb04
// 两次实测，手工接链两次）。修复：检测到"最新 cli 链尖落后于文件真实尖端"
// 时，追加一条 cli entrypoint 的谱系标记、parentUuid 指向文件尖端——最新 cli
// 链立即变为包含手机回合的链，下次 resume 直接可见。纯追加零改写，幂等
// （标记本身就是新的 cli 链尖，二轮检测即线性）。

// ccLineageProbe 是谱系分析的最小条目视图：只解链路三要素，其余字段不碰。
type ccLineageProbe struct {
	Uuid       string `json:"uuid"`
	ParentUuid string `json:"parentUuid"`
	Entrypoint string `json:"entrypoint"`
}

// appendLineageMarker 在 jsonlPath 上检测分叉并按需追加谱系标记。
// nativeSessionID/cwd 写入标记条目（sessionId/cwd 字段与 CC 原生条目对齐）；
// notice 为标记正文（面向人阅读的同步说明）。best-effort：读不到/不分叉一律
// 静默返回 nil，绝不阻塞回合收尾。
func appendLineageMarker(jsonlPath, nativeSessionID, cwd, notice string) error {
	jsonlPath = strings.TrimSpace(jsonlPath)
	if jsonlPath == "" || strings.TrimSpace(nativeSessionID) == "" {
		return nil
	}
	var order []*ccLineageProbe
	byUuid := map[string]*ccLineageProbe{}
	// 复用 v1.1.41 流式读取器：病态超长行跳过不冻结（该行 uuid 缺席只可能
	// 让祖先行走得更早停——最坏是多补一条标记，无害）。
	forEachAgentSessionJSONLLine(jsonlPath, agentVibeJSONLMaxLineBytes, func(line []byte) bool {
		var e ccLineageProbe
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		if e.Uuid == "" {
			return true
		}
		p := e
		byUuid[e.Uuid] = &p
		order = append(order, &p)
		return true
	})
	if len(order) == 0 {
		return nil
	}
	fileTip := order[len(order)-1]
	newestCli := (*ccLineageProbe)(nil)
	for i := len(order) - 1; i >= 0; i-- {
		if order[i].Entrypoint == "cli" {
			newestCli = order[i]
			break
		}
	}
	if newestCli == nil {
		return nil // 无交互史：resume 本就沿 sdk 链走，无需标记
	}
	if ccLineageIsAncestor(byUuid, fileTip.Uuid, newestCli.Uuid) {
		return nil // 线性：cli 链已含文件尖端
	}
	marker := map[string]interface{}{
		"parentUuid":  fileTip.Uuid,
		"isSidechain": false,
		"type":        "system",
		"subtype":     "informational",
		"content":     notice,
		"isMeta":      false,
		"timestamp":   time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"uuid":        ccPeerMsgID(),
		"userType":    "external",
		"entrypoint":  "cli",
		"cwd":         cwd,
		"sessionId":   nativeSessionID,
	}
	b, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("lineage marker: appended session=%s fileTip=%s cliTip=%s", nativeSessionID, fileTip.Uuid, newestCli.Uuid))
	return nil
}

// ccLineageIsAncestor 沿 parentUuid 自 from 向根走，报告是否途经 needle。
// 缺失父节点（跨文件引用/病态行被跳过）按根处理。
func ccLineageIsAncestor(byUuid map[string]*ccLineageProbe, needle, from string) bool {
	cur := from
	seen := map[string]bool{}
	for cur != "" && !seen[cur] {
		if cur == needle {
			return true
		}
		seen[cur] = true
		e := byUuid[cur]
		if e == nil {
			return false
		}
		cur = e.ParentUuid
	}
	return false
}
