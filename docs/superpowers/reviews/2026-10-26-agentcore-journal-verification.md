# agentcore journal 实现验证报告

日期：2026-10-26
验证对象：worktree `.claude/worktrees/agentcore-journal`，分支 `feature/agentcore-journal`
（验证期间 tip 从 89b7678 前移至 439a30f，共 **7 个 commit**）
验证方式：三路并行核查（写侧挂点 / 读侧接线与测试配对 / git 拓扑与合并风险），
全包 `go test ./app/http/services/ -count=1` 通过（112s，darwin）

## 总评

**实现质量高，验收通过**。spec §5-§9 实质契约全部满足，且多处比原计划更优：
去重键只记「已落盘成功」（写失败重试可补写）、目录懒创建覆盖 steer-先到场景、
prune 保护新建文件、end 行六出口逐出口核对全对、成功/错误出口均有端到端
journal 断言、分支纯增量零删除行（claude/codex 通道零改动）。

## 遗留项（按优先级，均可就近处理）

1. **[major｜合并时处理] 测试文件冲突**：并入当前 master（d300672 起 12 个新
   commit）必撞 `agent_ai_agentcore_run_test.go`（4 处冲突块，全为两侧文件尾部
   追加测试函数的落点重叠；函数名零重叠，机械取舍即可）。**解完冲突必须在
   合并结果上重跑 `go test ./app/http/services/`**——分支绿灯不覆盖合并态。
   生产代码全部自动合并且语义连贯（已通读合并后 runAgentCore）。

2. **[minor] candidate 枚举路的 `.aliang` 根未被测试锁定**：
   `agent_detail.go:267` 那一行删掉现有测试依然全绿（e2e 的 candidate 断言
   无判别力——candidate 循环空转后兜底循环照样命中）。修法一行：在
   `agent_detail_candidate_pattern_test.go` 的
   `TestCandidateAgentVibeSessionFilesMatchesCodexRolloutNames` 里加
   `<home>/.aliang/ai-sessions/<id>.jsonl` fixture，断言
   `candidateAgentVibeSessionFiles(sessionID)` 命中它。不修的后果（仅回归时
   发生）：带 sourceSessionID 的详情查询跌入 per-root 100-newest 全量解析兜底
   （codex 通道修过的那个数百 MB 坑），会话滑出兜底窗口即 not found。

3. **[minor｜可选] 残行粘行防护缺失**：`appendLineLocked` 用
   `O_APPEND|O_CREATE|O_WRONLY` 无末字节探测——崩溃残留半行后下一行粘合，
   每次掉电级事件恰好多丢 1 条完整行（有界不扩散）。按 spec §5「尽力而为」
   口径**可接受不修**。若加固：打开模式改 `O_RDWR`（Linux 对 O_WRONLY fd
   ReadAt/pread 恒返 EBADF，不能只改读方向；macOS 宽松会假绿——勿只在
   darwin 验证），写入前 `f.ReadAt(尾字节, size-1)` 非 `\n` 先补一个换行；
   并给 reader 测试补「残行+紧随完整行」粘行 fixture。

4. **[info｜可选] 带附件 steer 的 user 行口径**：journal 落的是追加附件后缀
   之后的 `steer.Content`，而 session.history 存原始 content——回放里该条
   user 消息带附件注入模板文本。一行改动可对齐（传原始 content）；不改也
   可接受（回放显示内核真实收到的文本）。

5. **[info] steer handler 接线无 handler 级测试**：`ai.steer → deliver 成功 →
   journalSteerUserMessage` 的 3 行接线只被 manager 方法单测覆盖；误删会静默
   丢 steer 行。spec §10 未要求，现状可接受。

## 已修正的文档矛盾

- 主仓 spec §7「无头行的损坏文件整文件跳过」与实现矛盾（实现容忍无头行：
  ID 兜底文件名，动机=steer 先到/头行写失败）——spec 已依实现修正（容忍语义
  +动机注明）；测试计划行同步修正。
- 主仓 plan（`2026-10-26-agentcore-journal.md`）顶部已加「已被实现取代」标注，
  勿按其执行；保留作设计过程存档。
- worktree 里的 spec/plan 未跟踪副本均已过期，以主仓为唯一权威（副本可删）。

## 合并拓扑速查

- 分支基于 master 4e705f8；master 自分叉点新增 12 commit（effort/图片/panic/
  steer 排空/API key/模型权威/裸 JSON 错误体），无一触碰
  agent_detail.go / agent_inventory.go / agent_vibe_digest.go；
- 唯一双侧同改的生产文件 `agent_ai_agentcore.go` 自动合并成功且语义连贯；
- master 的 016fa91（尾声排空排队插话）与分支 steer 落账不双写（016fa91 只在
  内核内 drain，不走 deliver 入口）；
- master 无 journal 半实现；7 个 commit 触碰面全部限定在 journal 功能文件。
