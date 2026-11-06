# 聊天图片全链路 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 手机/网页的图片消息全链路打通：持久附件缓存 → journal 附件化 → 读侧透传 → 本地取图/上传端点 → 网页渲染与发送。

**Architecture:** 新增持久缓存服务（会话键目录，写点=内核下载路径/steer/网页上传三处收敛）；journal 行增 `attachments` 数组（空文本守卫放行带附件行）；读侧三层透传 + claude/codex 原生转录 `[图片]` 占位；两个新本地端点（upload/取图，双注册+loopback+穿越守卫）；前端 useAliangChat 透传 + ChatComposer 上传 + ChatMessageBubble 附件分支（user 气泡纯文本铁律不破）。PhoneServer 零改动。

**Tech Stack:** Go 标准库（multipart/filepath 守卫）、Vue3 options/composition、vitest。

**Spec:** `docs/superpowers/specs/2026-11-05-chat-image-pipeline-design.md`

**提交纪律（项目 CLAUDE.md）：** 不自动 commit；完成后汇报等拍板。

---

### Task 0: worktree（已完成）

- [x] `.worktrees/chat-image-pipeline`（分支 `feature/chat-image-pipeline`，基线 d4ed54a），dist 已拷。基线命令：`go build ./app/http/services/`。

---

### Task 1: 持久附件缓存服务

**Files:**
- Create: `app/http/services/agent_ai_attachment_persist.go`
- Create: `app/http/services/agent_ai_attachment_persist_test.go`

- [ ] **Step 1: 写失败测试**（要点：会话键目录创建/相对引用往返/穿越拒绝/引用解析回绝对路径；`t.Setenv` 控 home）

```go
// agent_ai_attachment_persist_test.go
func TestAgentAttachmentPersistRoundTrip(t *testing.T) { /* Persist 写入→RelPath→Resolve 回绝对路径→内容一致; 会话子目录自动建; 文件名净化 */ }
func TestAgentAttachmentPersistRejectsEscape(t *testing.T) { /* ref 含 .. / 绝对路径 / 根外路径 → Resolve 报错 */ }
func TestAgentAttachmentPersistSweepCap(t *testing.T) { /* 造 2 会话缓存超全局帽 → sweep 驱逐最旧会话 */ }
```

- [ ] **Step 2: 红**

```bash
go test ./app/http/services/ -run 'TestAgentAttachmentPersist' -count=1
```
Expected: FAIL（undefined）

- [ ] **Step 3: 实现**（新文件；全部小函数可测）

```go
package services

// 持久附件缓存：手机/网页图片消息的网页显示真值源（spec 2026-11-05-chat-image-pipeline §3.1）。
// 目录 <EffectiveAgentHome>/aliang-attachments/<sessionID>/；journal 只落相对引用；
// 网页经 /api/agent/ai/attachment?ref= 取图（穿越守卫在端点+Resolve 双层）。
// 配额：单文件 ≤10MiB（上传端点钳）、单消息 ≤8（载荷钳）、会话 ≤256MiB（写入拒绝）、
// 全局 ≤1GiB（启动 sweep 按会话最旧驱逐）。
const (
	agentAttachmentCacheDirName    = "aliang-attachments"
	agentAttachmentFileMaxBytes    = 10 << 20
	agentAttachmentSessionMaxBytes = 256 << 20
	agentAttachmentGlobalMaxBytes  = 1 << 30
)

func agentAttachmentCacheRoot() (string, error)   // <EffectiveAgentHome>/aliang-attachments（复用 EffectiveAgentHome 解析）
func agentAttachmentSessionDir(sessionID string) (string, error) // root/<sanitized sessionID>，MkdirAll 0755
func agentAttachmentPersist(sessionID, base string, ext string, src []byte) (rel string, err error) // 写文件→返回相对引用
func agentAttachmentResolve(rel string) (string, error) // Clean+Join(root)→agentPathInsideDirectory 守卫→Stat
func agentAttachmentSweep() error // 全局帽：stat 全会话→超帽按会话 mtime 最旧驱逐整目录
```

实现细节：`sanitizeAgentAttachmentBase`（只留字母数字连字符下划线点，截 80 rune）；ext 白名单（.jpg/.jpeg/.png/.gif/.webp，缺省 .bin 不入库）；`agentPathInsideDirectory` 复用 execution_guard.go 既有函数。

- [ ] **Step 4: 绿**

```bash
go test ./app/http/services/ -run 'TestAgentAttachmentPersist' -count=1
```

---

### Task 2: 三个写点接线

**Files:**
- Modify: `app/http/services/agent_ai_attachment_download.go`（runCLI 下载路径落副本）
- Modify: `app/http/services/agent_ai_claude_image.go`（buildSteerImageBlocks 下载点落副本）
- Modify: `app/http/services/agent_ai.go`（agentAIAttachment 增 PersistPath 字段；runCLI 调用点透传 sessionID）

- [ ] **Step 1: `agentAIAttachment` 增 `PersistPath string`（相对引用）字段（:600-606 区段）**
- [ ] **Step 2:** runCLI 的 downloadAgentAIAttachments 成功后逐附件 Persist（sessionID 从 run 取；失败仅 Warn 降级——持久化失败不得影响内核收图）；steer 路径同点接线。
- [ ] **Step 3:** 验证 `go build ./... && go test ./app/http/services/ -run 'Attachment' -count=1`

---

### Task 3: journal 写侧附件化

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_journal.go`（Ref 类型+appendUser/appendMsg+守卫）
- Modify: `app/http/services/agent_ai.go`（两处落账调用点 :2596/:4698 透传）

- [ ] **Step 1: 失败测试**（追加到 agent_ai_agentcore_journal_test.go）：纯图行落盘（text 空+attachments 非空→行存在且带 attachments）；带字行 attachments 同落；老五行（无附件）schema 不变。

- [ ] **Step 2: 红** → **Step 3: 实现**：

```go
type agentAIJournalAttachment struct {
	Name    string `json:"name"`
	Mime    string `json:"mime,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Ref     string `json:"ref"` // 相对缓存根引用
	Origin  string `json:"origin,omitempty"` // phone|web
}
```
- `appendMsg`/`appendUser` 签名追加变参 `attachments ...[]agentAIJournalAttachment`；守卫 `strings.TrimSpace(text)=="" && len(attachments)==0 → return`；行 map 增 `"attachments"`（非空才写）。
- **签名变更触及的 5 个调用点**（spec §3.2 修订版清单）：
  1. `agent_ai_agentcore.go:1153` runAgentCore 消息主路径——**主写点**，附件从 run.attachments 透传（PersistPath 非空才入 refs；Origin=phone）；
  2. `agent_ai.go:4698` 区段 journalSteerUserMessage——steer 附件透传；
  3. `agent_ai_agentcore.go:712`（/compact）、:1756（auto-compact）、`agent_ai.go:2596`（本地斜杠命令）——纯文本调用点，变参缺省零改动。
- 缓存文件名按 messageID-idx（会话无 messageID 用 runSeq 兜底）。

- [ ] **Step 4: 绿**（新测试+既有 journal 测试不改自绿）

---

### Task 4: 读侧透传

**Files:**
- Modify: `app/http/services/agent_ai.go`（agentAIMessage 增 Attachments 视图字段）
- Modify: `app/http/services/agent_ai_journal_history.go`（msg/turn 两 case 透传+空文本带附件行保留）
- Modify: `app/http/services/agent_ai_sockets.go`（快照透传）
- Modify: `app/http/services/agent_inventory_agentcore.go`（reader 透传）
- Modify: `app/http/services/agent_inventory.go`（claude/codex image block → `[图片]` 占位）

- [ ] **Step 1: 失败测试**（journal_history：纯图行→hist 含 attachments 且 Content==""；inventory：claude content 数组 image block → "[图片]" 文本入 transcript）
- [ ] **Step 2: 红** → **Step 3: 实现**（视图结构 `agentAIAttachmentView{Name,Mime,Bytes,Ref}`；行解析 `row["attachments"]` → []view；快照行同步加键；inventory 只做占位不透 URL） → **Step 4: 绿**

---

### Task 5: 本地端点 upload/取图 + 双注册

**Files:**
- Create: `app/http/handlers/agent_handler_attachment.go`（两个 handler）
- Modify: `app/http/handlers/agent_handler.go` 或 handler 接线处、`app/http/routes/routes.go`、`app/agentruntime/server.go`

- [ ] **Step 1: 失败测试**（handler 直测：httptest multipart 上传→ref→GET 取回内容一致；超 10MiB 4xx；mime 不符 4xx；ref 穿越 4xx；Origin 跨源拒绝——照抄 transfers D13 模式）
- [ ] **Step 2: 红** → **Step 3: 实现**：

```
POST /api/agent/ai/attachment/upload   → loopback+Origin 守卫 → multipart session_id 字段（非空校验）+ mime/size 钳 → agentAttachmentPersist → JSON {ref,name,mime,bytes}
GET  /api/agent/ai/attachment?ref=     → agentAttachmentResolve → Content-Type by ext/mime → http.ServeContent
```
（附带可观察测试：10-20MiB 带内文件「进内核但跳过持久化」的降级口径钉——spec §3.1。）
- [ ] **Step 4: 双注册** routes.go `register(...)` + server.go `mux.HandleFunc(...)`（两处同名路径；aa80152 教训）
- [ ] **Step 5: 绿 + `go build ./...`**

---

### Task 6: resolveAgentAIAttachments 缓存根守卫扩展

**Files:**
- Modify: `app/http/services/agent_ai.go`（:3598-3600 项目目录守卫 → 项目目录 或 附件缓存根）

- [ ] **Step 1: 失败测试**（path=缓存根内绝对路径 → 解析通过且 URL 清空/PersistPath 置位；缓存根外非项目路径 → 仍拒绝）
- [ ] **Step 2: 红 → 实现 → 绿**（agent_ai_attachment_download_test.go 同族追加）

---

### Task 7: 前端 useAliangChat 透传+发送

**Files:**
- Modify: `app/website/src/composables/useAliangChat.js`
- Modify: `app/website/src/services/agentApi.js`（uploadChatAttachment(file) helper）

- [ ] **Step 1: 失败测试**（useAliangChat 现有测试模式追加：normalization 保留 attachments→映射 `{name,mime,bytes,url:'/api/agent/ai/attachment?ref='+ref}`；send payload 含 attachments；**空文本带附件行在 transcript/snapshot 两处归一化后仍保留**）
- [ ] **Step 2: 红 → 实现**：
  - transcript/snapshot/detail 三处归一化保留附件（ref→url 组装函数抽纯函数导出 `chatAttachmentUrl(ref)` 供组件复用）；
  - **第四丢图点修复**：`applyDeviceTranscript`（~:771-780）与 `applyDeviceSnapshotTranscript`（~:793+）的 `m.content`/`m.text` truthy 过滤改为「空文本但 attachments 非空也保留」；空文本纯图行进快照与详情转录，但**不进续聊 deviceTranscript**（内核续聊仍纯文本语义）；
  - 发送：`sendMessage(conv, text, attachments)` 载荷加 `attachments:[{type:'image',name,path}]`；上传中 `sending` 守卫；**edit-resend（~:1800）只重发文本不带附件**（对齐 content-only 编辑语义）；
- [ ] **Step 3: 绿**（`npx vitest run src/composables/useAliangChat.test.js`——若无测试文件则按仓内先例建）

---

### Task 8: ChatComposer 上传 UI

**Files:**
- Modify: `app/website/src/components/chat/ChatComposer.vue`

- [ ] **实现要点**（组件契约）：附件按钮（回形针 icon，min-h-10 触控）→ `<input type=file accept="image/png,image/jpeg,image/gif,image/webp" multiple>`（隐藏）；选中即逐个 upload（agentApi.uploadChatAttachment），chips 显示缩略+移除；≤8/10MiB 客户端预检（超限走 i18n 提示）；上传在途 emit 禁发态；send 时把 chips 的 `{type,name,path}` 随 @send 上抛；发送成功清 chips。emit 形状与 ChatPage 现有 `@send="sendMessage"` 对齐（sendMessage 增第二参数，默认兼容）。
- [ ] **验证**：`npx vitest run`（新增 ChatComposer 附件流用例：mock upload、超限提示、发送载荷断言）

---

### Task 9: ChatMessageBubble 附件渲染

**Files:**
- Modify: `app/website/src/components/chat/ChatMessageBubble.vue`

- [ ] **实现要点**：props/消息对象带 `attachments` 时，在文本块相邻渲染缩略图条（`<div class="flex gap-2 overflow-x-auto py-1">` + `<img :src="a.url" class="max-h-40 w-auto rounded-lg object-cover cursor-zoom-in" loading="lazy" @click="openImage(a.url)">`，`openImage=window.open`）；`text` 为空且仅图时不渲染空文本节点；**user 气泡文本仍是 `{{ msg.content }}` 插值——铁律不破**（附件为兄弟元素）；暗色边框适配；`min-h-10` 触控无关（图片点击区 ≥40px 由 max-h-40 保证）。
- [ ] **测试**：ChatMessageBubble 现有测试模式追加附件分支用例（createApp 挂载断言 img 数/铁律纯文本不变）
- [ ] **验证**：`npx vitest run` 全绿 + `npx playwright test tests/e2e/mobile-responsive.spec.js` 绿

---

### Task 10: i18n + 全量验证 + 汇报

- [ ] **i18n**：zh/en `chat_attach_add:'添加图片'/chat_attach_tooMany/chat_attach_tooLarge/chat_attach_uploadFailed/chat_attach_unsupported`（camelCase 平铺，随现有 chat_* 前缀）
- [ ] **全量**：`go build ./...`；`go test ./app/http/services/ -count=1`（对照 flake 基线：TestJournalTruncate… 预存失败 dc2bfa0 带入勿误判）；`go test ./cmd/ -count=1`；前端 `npx vitest run` + mobile e2e
- [ ] **汇报**：改动摘要+真机验收清单（手机发纯图→网页显示；网页发图→内核描述内容→刷新仍在），等拍板合并

---

## 风险锚点（实现者必读）

1. **user 气泡纯文本铁律**（ChatMessageBubble.vue:81-84 注释）：附件渲染=兄弟 DOM 分支，绝不走 markdown 管线。
2. **穿越守卫三处**：Resolve/取图端点/上传端点，每处都要测试用例。
3. **双注册**（routes.go+server.go）漏一 = core 代理 404。
4. **内核注入路径一行不动**：持久化纯附加，失败 Warn 降级（无网页显示≠内核失图）。
5. **journal 行加键兼容**：老 reader map 索引忽略未知键——全包测试实证（对照 flake 基线：TestJournalTruncate… 预存）。
6. **前端在途禁发**；WS 重连 chips 保留。
7. worktree 构建：embed dist 已拷；gopls worktree 误报无视；4174 旧 server 冒烟陷阱。
