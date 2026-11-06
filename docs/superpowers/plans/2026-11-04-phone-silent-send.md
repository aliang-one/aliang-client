# 待办问题档案:手机 vibe 对话发送静默失败(新 Windows 装机)

- **状态**:未定谳,等受影响机器取证输出(2026-11-02 立档,2026-11-04 归档待办)
- **报告人复述**:新电脑登录 alianggate 后,在**手机**上创建指向「根目录」的 vibe coding 对话,消息发不出去,**没有任何错误提示**(连转圈报错都没有)
- **与桌面故障的关系**:同一天报告的另一族故障,**已排除**是桌面 native id mismatch 的延伸(见下「定界」);桌面 mismatch 已修复并推送(master bc72ee7,生效待打新 tag 出包)
- **关联记忆**:`~/.claude/projects/.../memory/aliangcode-new-machine-empty-model-session-flood.md`(桌面定案全文+取证命令背景)

---

## 1. 环境事实(2026-11-02 取证时点)

- 受影响机:全新 Windows,alianggate **v1.1.91**(tag 号倒挂:比 v1.1.93 晚打、内容更新,但仍落后 master)
- 该机桌面端 aliangcode 内置内核:第一条消息成功,第二条起每条必败(=native id mismatch,已修 bc72ee7)
- v1.1.91 缺失且与本问题直接相关的 master 补丁:
  - `81739b5`(v1.1.94):**家目录/盘根遍历硬拦 + 工具超时通知用户**——缺它则「根目录会话模型首个工具动作全盘扫描」会无声挂死最长 1h
  - `4e14dfb`:卡死工具组杀
  - `7fe0e6d`:审批请求投递 journal(L1 可靠投递)——缺它则审批卡可能丢失→会话卡 running
  - 结论:**升级本身是该问题的「半解药」**,候选 C/E 在新包里已被缓解
- ⚠️ master 在立档后仍被并行会话推进(bc72ee7 之后另有批次),动手前先核对 `git log` 现状

## 2. 定界:为什么不是桌面那个 mismatch

1. 手机走 **v3 通道**(ai.run.start,`agentPublish.ts:191` 不先发 create),而 mismatch 的断点在 v2 `create()` 既有分支(agent_ai.go:1959,v3 已有 `mergeRunStartIdentityLocked` 同款纪律)→ 手机对该 bug **免疫**
2. 手机**新建会话没有绑定记录**,mismatch 条件(期望非空且不等)构不成
3. 错误帧链路逐环核实**通畅**:agent 终态闸(agent_ai.go:641-651+投递 journal)→ PhoneServer raw-forward(`handler.ts:993-1024`,唯一转写是 transient 识别,不匹配 mismatch 文案)→ 手机 ai.error→failed 渲染(`aiSessionSlice.ts:1229`)——若 mismatch 真发生,手机会有红卡,不会「无提示」

## 3. 候选判定表(按概率排序,定谳靠取证)

| 候选 | 机制 | journal/日志区分指纹 |
|---|---|---|
| **A. 僵尸回合堵队列** | 该机桌面 mismatch 回合或卡死工具把会话留在 running,手机新消息被转 steer 进死队列(agent_ai_agentcore.go:1873-1886 deliver 恒 queued;expire 豁免 :1926-1933)或撞 already-running(agent_ai.go:2290-2293,PS 转写「Device still had a previous turn running」并自动下发 ai.stop,aiRun.ts:931-960) | 旧会话 jsonl 有 user/turn **无 end 行**;agent.log `ai.run: session=` 有起跑无终态;`ai.steer.stalled`(5min 后) |
| **B. run.start 持久化 claim 失败静默丢** | agent 本地 `.aliang\agent\` 写失败(新装机杀软/权限高发)→零帧丢弃仅 Warn(agent_ai.go:2374-2379) | agent.log **`durable claim failed`**(一票定谳);outbox 该 run_id 恒 pending;journal **连 user 行都没有** |
| **C. 根目录→首工具动作全盘扫描挂起** | 起跑成功、模型第一个 shell/glob 打在 C:\ 全盘爬;v1.1.91 无硬拦无超时提示 | journal 有 user 行 + 卡在单条 `tool` 行不动;1h 后 `[ai.run.kill] reason=tool_wait_ceiling`;手机侧若有 ai.command 瘦身包流动则是 C |
| **E. 不可见审批卡钉死 running** | 审批请求手机没显示;**pending 审批豁免 stale sweep**(sweepers.ts:196-201)→永不判死直到 agent 审批超时 24h(execution_guard.go:122) | `ai_approval_requests.json` 有 pending;running 时长 >10min 仍不判死即为 E |
| F. agent WS 读循环同步阻塞 | ai.run.start/message/create 在读循环内同步跑无 safeGo(agent_remote_ws.go:928-1004),慢盘/死锁停摆全帧但心跳独立→服务端看设备仍「在线」 | pong/ack 全停;重启 agent 进程立刻恢复即为 F |

## 4. 静默点全景(S 表,修复候选参考)

| # | 静默点 | 位置 |
|---|---|---|
| S1 | durable claim 失败零帧丢弃(只 Warn) | agent_ai.go:2374-2379;落盘 agent_ai_identity.go:124-149 |
| S2 | 已 claim 未终态的重投全部静默吞(replayProcessedRun 无终态→false→静默 return) | agent_ai.go:2366-2384;agent_ai_identity.go:150-156 |
| S3 | 会话 running 再发→already running(有帧但 PS 转写) | agent_ai.go:2290-2293 |
| S4 | steer 进死队列 deliver 恒 queued + expire 对等待态豁免 | agent_ai_agentcore.go:1873-1886/1926-1933 |
| S5 | AI 臂读循环内同步执行无 safeGo 无 recover | agent_remote_ws.go:928-1004(对照 :841/:844 有 safeGo 先例) |
| S6 | emitter 终态闩后一切帧静默丢 | agent_ai.go:674-676 |
| S7 | 断连窗口非终态帧直接丢(终态靠 journal 补投) | agent_remote_ws.go:449-455/707/734 |
| S8 | 服务端 outbox 永久 pending(FIFO 重投) | PS outbox.ts:69-131/148-168 |
| S9 | 409/4xx 有回帧但手机可能不渲染 ai_session_busy | runState.ts:25;mobile/handler.ts:462-468 |
| S10 | steer ack 只回 origin 写 | agent_ai.go:3005-3025 |

## 5. 取证清单(在受影响 Windows 机上执行,输出贴回即可定谳)

```powershell
# ① journal 全景:每个会话文件的行类型统计(有 user/turn 无 end = 僵尸回合)
Get-ChildItem "$env:USERPROFILE\.aliang\ai-sessions" -Filter *.jsonl -EA SilentlyContinue | Sort LastWriteTime -Desc | Select -First 8 | % {
  "### $($_.Name)  $($_.LastWriteTime)"
  Get-Content $_.FullName | % { $_ | ConvertFrom-Json } | Group-Object kind | Select Name,Count | Format-Table -Auto
  Get-Content $_.FullName -Tail 2
}

# ② agent 日志关键指纹(agent.log 在 %USERPROFILE%\.aliang\agent\ 下)
$log = "$env:USERPROFILE\.aliang\agent\agent.log"
Select-String "durable claim failed|ai\.run\.kill|force_finalize|ai\.steer\.stalled|already running|native session id mismatch" $log | Select -Last 30

# ③ 投递/审批状态文件(非空=终态发了没到 / 有审批卡没人看到)
Get-Content "$env:USERPROFILE\.aliang\agent\ai_run_terminals.json" -EA SilentlyContinue
Get-Content "$env:USERPROFILE\.aliang\agent\ai_approval_requests.json" -EA SilentlyContinue

# ④ 端口快照
netstat -ano | findstr "56431 56432 56433"
```

判读:①无 user 行→B;有 tool 行卡死→C;有 user/turn 无 end→A;③第一文件非空→S7 半开、第二文件非空→E。注意:agent 若由 LocalSystem 服务链拉起,journal 根在 `C:\WINDOWS\system32\config\systemprofile\.aliang\ai-sessions\`,两个根都要看。

## 6. 修复方向预判(定谳后再动手)

- **C** → 升级即缓解(v1.1.94 `81739b5` 已有硬拦+超时提示);若还要根治:v2 面补「盘根/家目录 cwd 警示帧」
- **E** → v1.1.91 缺的 `7fe0e6d` 审批投递 journal 是解药;升级后仍需核对手机端审批卡渲染
- **A** → 核对 master 僵尸家族现状(fix/agentcore-zombie-fast-kill 是否已合);steer 死队列应有超时发声
- **B** → S1/S2 静默丢改「延迟终态+可重试」:claim 写失败至少给服务端一个可重试信号,而不是永久 pending
- **F** → AI 臂 safeGo 化(对照 :841 先例),读循环永不因单命令阻塞

## 7. 处理顺序建议

1. 先让用户在受影响机跑 §5 取证,按 §3 表定谳
2. 定谳后按 §6 对应分支开 worktree(TDD,遵守提交协议)
3. 无论哪个候选:**先让该机升级到含 bc72ee7 与 81739b5/4e14dfb/7fe0e6d 的新包**再复测——大概率直接消失或指纹大幅收窄
