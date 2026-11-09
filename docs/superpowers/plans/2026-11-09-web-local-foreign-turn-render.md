# 计划:网页本地会话跨端回合渲染——多答连体+提问隐形根治(2026-11-09)

## 事故(vm22 定案,详见 memory web-local-view-foreign-turn-merge)

手机端向网页正在使用的同一 journal 会话发问(origin=phone,user 行 `msg_*` id),
网页本地会话视图:①快照对账把锚点之后**所有** assistant 行(含外来回合)拼进
最后一个气泡=多答连体;②外来 user 行永不回建气泡=提问隐形;③活体 delta 在
旧泡滞留 running 时续写旧泡(挤一泡)、末泡 done 时直接丢弃(回答隐形)。
journal 数据面无误;病灶全在 `useAliangChat.js` 本地会话路径的「单写者假设」。

## 设计原则

journal 是会话结构的唯一权威;本地视图是缓存+装饰。快照对账从「启发式嫁接」
升级为「回合分段 + 权威修复 + 外来回合补建」,沿仓内既有「纯函数内核 + 薄壳
装配」模式(对齐 deviceSnapshotTranscriptToMessages 的测试接缝)。

## 改动清单

### A. 新纯函数 `reconcileLocalSnapshotMessages(localMessages, rows, opts)`
(useAliangChat.js 导出,可单测;opts: provider/snapshotRunning/fallbackTs)

- rows(快照转录,已折叠为 user/assistant/system 行)按 user 行切**回合**。
- 锚=最后一个「userRow.message_id 命中本地 user 气泡」的回合;无锚→no-op
  (安全语义保留,现有 turnIsOurs=false 行为不变)。
- 锚点前区域不动;锚回合 assistant 文本按**方向化前缀修复律**收敛:
  - 回合已定谳(存在后续回合,或快照 status≠running):
    `journal⊇local` 或 `local⊇journal`(连体垃圾特征)→ 以 journal 文本替换
    并置 done;双向皆非前缀(分叉/截断标记)→ 保守不动。
  - 回合在流(末回合+running):仅 `journal⊇local` 增长式对齐(现有 canAlign
    语义);绝不收缩(半回合 delta 不许丢)。
  - 锚回合无气泡且 journal 有文本 → 补建(吸收现有 noBubble 分支)。
- 外来回合逐个补建:user 气泡(message_id/provider/ts/attachments 透传;
  `.option` 后缀行跳过——翻卡即作答,补泡会与卡重复)+ 回合答案气泡
  (末回合且 running→running,余→done)+ end(error) 系统条照 journal 序。
- 尾随 running 气泡归属判定:snapshotRunning 且内容为锚回合文本前缀→锚回合
  流泡;否则归末个外来回合(活体 delta 先于快照落泡的竞态)。
- 无 journal 对应物的本地装饰(系统条/失败发送)按稳定序保留在重建尾部
  (位置近似,注释声明)。

### B. 帧处理器(composable)

1. `ensureAssistantBubble`:run 窗口感知——`conv.runActive` 时不做空泡丢弃,
   必产新泡(修复外来回合/选项作答回合 delta 丢弃)。
2. `ai.run.started`:关闭前一个 running 气泡(服务端串行回合语义=权威收敛,
   根治滞留 running 类);`conv.runActive=true; conv.running=true`;本地会话
   (matchedById 且非壳)发 `ai.session.subscribe` → 快照补建外来提问行。
3. `ai.done/ai.error`:`conv.runActive=false`;本地 `conv.running=false`;
   done 时若有 running 气泡被收敛(或 recoveredRun)→ 复订阅做终态定谳快照。
4. `isConversationRunning`:并上 `conv.runActive`(外来回合在跑时本端消息
   走 steer 分流=语义正确)。
5. `reconcileLocalSnapshot` 改为薄壳:inFlight 守卫/看门狗再武装/审批 upsert
   保留;调纯函数;changed→saveToStorage+scrollToBottom;定谳→finishRun。

### C. 测试(TDD)

- 纯函数组(新文件 useAliangChat.localRound.test.js 或并入 reconcile.test.js):
  连体修复/外来补建/流泡保护/方向化前缀律三分支/.option 跳过/附件透传/
  失败发送保留/无锚 no-op/装饰保留。
- 帧组:run.started 关旧泡+runActive;delta 空窗产泡;done 清窗;
  run.started 触发订阅;isConversationRunning 并窗。
- e2e(chatStub):外来回合快照+活体流全程,断言提问可见、答案分泡。

### D. 不做(范围钉死)

- 服务端不动(数据面/帧协议已正确);设备壳路径不动(已权威重建);
- draft 打开时全量重建路径不动(既有);超 200 行快照帽的头部外来回合不补
  (帽内修复,注释声明;草稿全量路径可治)。

## 验收

vm22 场景三断言:①外来提问气泡可见(messageId=journal 同源,可作撤回锚);
②各回合答案独立成泡、连体垃圾被修复;③活体外来回合不再丢 delta/不续旧泡。
vitest 全绿 + e2e 新 spec 绿 + 既有 305+ 测试无回归。
