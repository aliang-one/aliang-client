# 设备互访缺陷批次修复设计（D1-D4）

- 日期：2026-11-02
- 来源：25-agent 全链路复核（感知/执行/AI 工具面/文件传输/隧道五路探查 + 逐条对抗复核），发现 4 项缺陷
- 范围：**仅本仓（agent 客户端）**。PhoneServer zod/中转与手机 App UI 为后续仓接项
- 状态：设计定稿（用户已批准方向），待评审

## 背景

设备互访（peer-link）阶段 1+2 已上线（v1.1.88~v1.1.91，PhoneServer v1.0.157）。全链路复核确认四项缺陷：

| 编号 | 缺陷 | 位置 |
|---|---|---|
| D1 | CLI provider（claude CLI 平台通道直跑）注入的 `aliang` MCP bridge 只注册了 `ask_user`，peer 五工具缺位（agentcore 内核与外部 `aliang mcp` stdio 面均有） | `agent_ai_tool_ask.go:219-261`（`buildMCPServer`） |
| D2 | `transfer.receive` 帧实际携带 `direct_candidates`（`agent_transfer_receive.go:83` 在读），协议契约表漏登记 | `agent_protocol.go:335` |
| D3 | file.* 帧族只有 `file.list/read/upload`，手机端原生「远程编辑」无协议支撑 | 帧族缺 `file.write` |
| D4 | `file.upload` 的 `upload_url` 无任何 host 校验直接流式 PUT，而互传通道全部过 `agentTransferHostAllowed`（https+`*.myqcloud.com`）硬钉——同族不一致（spec 2026-10-25 §9 自认的历史现状） | `agent_file_upload.go:60+` |

## D1：peer 五工具注入 CLI provider（MCP bridge 补齐）

### 方案（已定）

在 `buildMCPServer()`（`agent_ai_tool_ask.go`）中追加注册五个 MCP 工具，handler 复用现有 payload 函数。**命名**：本桥统一全前缀下划线风格——这是**与 stdio 面（`cmd/mcp.go`）命名风格对齐的约定**，不是 SDK 硬约束（在用 go-sdk v1.8.0 的 `AddTool` 校验允许点号）；agentcore 桥的点号名（`peer.exec` 等，`agent_peer_tools.go:46-50`）是内核侧既有名空间，跨面不做字面统一，靠 D1 映射表对齐：

| 本桥 MCP 工具名 | 复用入口 | 输入 |
|---|---|---|
| `peer_exec` | `agentPeerExecPayload`（`agent_peer_tools.go:828`） | `device_id`、`command`；可选 `timeout_ms`、`workdir` |
| `peer_fetch_file` | `agentPeerFetchFilePayload`（:849） | `device_id`、`remote_path` |
| `peer_device_info` | `agentDeviceInfoPayload`（:869） | `device_id` |
| `peer_device_list` | `agentPeerDeviceListPayload`（:884） | 无 |
| `peer_device_note` | `agentDeviceNotePayload`（:899） | `device_id`、`key`、`value` |

### 关键性质（全部继承现状，零新增面）

- 注入面：**claude 族 stream-json 回合**（`outputFormat == agentAIOutputClaudeStreamJSON` 门槛，`agent_ai.go:6720`，注入调用 :6720-6741 区间）且非 readOnly/goal-fork 者自动收到 bridge 的 `--mcp-config` 注入，无需新增注入点。codex/opencode CLI provider（各自 outputFormat 分支 :8942/:9140）本就不注入 bridge，不在 D1 范围内——未来若其走平台通道缺 peer 工具，属新需求不属本批回归。
- 鉴权：沿用 bridge 既有 per-run token + loopback-only HTTP 面；出网凭据（JWT+device ID）仍在 agent 进程，CLI/LLM 接触不到。
- 门禁：目标设备 `peer_exec_enabled`（默认关）+ 云端 peer ACL + 全量审计，对 MCP 路径同样生效（payload 函数与注册表路径走同一服务实现）。
- readOnly 口径：bridge 在 readOnly/goal-fork 回合本就不注入——与内核「readOnly 剥离 peer 工具」（`agent_ai_agentcore.go:1394` 调用方门控）一致，无需额外分支。
- MCP `tools/call` 的参数是 JSON 字符串：bridge handler 解析为 `map[string]interface{}` 后直接喂 payload 函数；返回 `{result|error}` 文本进 `mcp.CallToolResult`（isError 语义照 `ask_user` 先例）。
- request_id：bridge 侧合成（`mcp_<uuid>` 短前缀），不与 WS 注册表路径冲突。

### 工具描述与三面命名对齐

三处工具面用**三套命名**（评审复核确认），不能做字面等集断言：

| agentcore 桥（点号，`agent_peer_tools.go:966+`） | 本桥 MCP（全前缀下划线） | stdio 面（`cmd/mcp.go:229+`，历史现状：后三个无 peer_ 前缀） |
|---|---|---|
| `peer.exec` | `peer_exec` | `peer_exec` |
| `peer.fetch_file` | `peer_fetch_file` | `peer_fetch_file` |
| `peer.device_info` | `peer_device_info` | `device_info` |
| `peer.device_list` | `peer_device_list` | `device_list` |
| `peer.device_note` | `peer_device_note` | `device_note` |

- 描述文本：本桥照抄 agentcore 桥的 description 与入参说明，代码注释交叉引用三处位置。不做提取重构（三处 schema 格式不同：内核 map / MCP JSON schema ×2）。
- **stdio 面不改名**：`claude mcp add` 存量用户的工具引用会被改名破坏，且 stdio 面不在本批范围。
- 防漂移测试=**显式映射表断言**（非字面等集）：services 侧断言「本桥名 ↔ agentcore 点号名」按上表一一对应；cmd 侧（`cmd/mcp_test.go`）断言「stdio 名 ↔ 本桥名」按上表一一对应。映射表本身写在各自测试里，任何一面加/删/改名时对侧测试红。

### 非目标

- 不改 tier 语义（不按 isolated/sanitized/full 区分 peer 工具可见性；门禁在服务端）。
- 不给 bridge 路径新增 journal 行为变更（如注册表路径已有 peer-exec.jsonl kind=tool 审计，MCP 桥路径审计为可选增强，见「遗留」）。

## D2：契约表补登记 `direct_candidates`

`agent_protocol.go:335` `AgentEventTransferReceive` 的 `Optional` 追加 `"direct_candidates"`（对齐 `:271` `transfer.result` 已登记 `via`/`lan_fail_stage` 的先例——接收 handler `agent_transfer_receive.go:83` 实际读该键）。

契约测试锚：`agent_transfer_contract_test.go` 增加 optional 字段断言（`transfer.receive` 必须登记 `direct_candidates`），与既有「四点齐」风格一致。

## D3：`file.write` 帧族（本仓全量）

### 帧契约（additive，不动 protocol_version）

常量（`agent_protocol.go`）：

- `AgentEventFileWrite = "file.write"`（server→agent 指令）
- `AgentEventFileWriteResult = "file.write.result"`（agent→server 终态）

ServerSends 契约条目：

```
{Type: AgentEventFileWrite,
 Required: []string{"type", "request_id", "project_path", "path", "content"},
 Optional: []string{"encoding", "expected_sha256"},
 Emits: []string{AgentEventFileWriteResult, AgentEventFileError}}
```

结果帧 `file.write.result`：`Required: {type, request_id, ok}`，`Optional: {path, size_bytes, sha256}`；失败复用既有 `file.error`（`agentFileErrorPayload` 形状），错误细分用 `error` 文本 + 结构化 `error_code`（见下）。

**同批契约补登记**：`AgentEventFileError`（`agent_protocol.go:281`，现状 `Required {type, request_id, error}`、**Optional 为空**）的 `Optional` 追加 `"error_code"`——D3 错误码全集（含 `bad_request`）与 D4 的 `upload_url_not_allowed` 全走此帧，不登记则重演 D2 式「handler 实读键 vs 契约登记」漏网（仓内无自动化一致性校验），且 PhoneServer zod 若剥未知键，结构化错误码到不了手机。

### handler（新文件 `agent_file_write.go`，模式照 `agent_file_upload.go`）

处理序（全部同步完成，单文件毫秒级，不进单飞槽）：

1. **解析**：`request_id/project_path/path/content/encoding/expected_sha256`。
2. **路径守卫**（与 `file.read` 同源 `resolveAgentProjectContentPath`，`agent_detail.go:554`）：
   - 目标已存在：`EvalSymlinks` 后过 `tccguard.GuardPath`（现函数已含此逻辑）；
   - 目标不存在（新建文件场景）：对父目录做 `EvalSymlinks`+`GuardPath`，文件名组件原样使用；**父目录必须存在且在守卫内，不做递归 mkdir**；
   - 守卫失败 → `file.error`，`error_code="path_forbidden"`。
3. **编码**：`encoding` 可选 `utf8`（默认）| `base64`（`stdEncoding`，二进制小文件）；未知值 → `error_code="bad_encoding"`。解码失败 → 同码。
4. **大小上限**：解码后 > `agentFileWriteMaxByte = 512 * 1024` → `error_code="too_large"`（与 `file.read` 的 512KiB 对称；大文件走互传通道）。
5. **乐观锁**：`expected_sha256` 非空时比对盘上现文件的 sha256；不符 → `error_code="conflict"`（防手机编辑与 AI 回合并发互踩；不带即最后写赢）。
6. **原子写**：同目录 `CreateTemp` → 写全量 → fsync → `rename` 覆盖（已存在文件沿用原 mode；新建 0644）。磁盘满/写失败 → `error_code="io_error"`。
7. **回帧**：成功 `file.write.result {request_id, ok:true, path, size_bytes, sha256(写后)}`；任何失败 `file.error` 携带 `error_code`。

### 分发与门禁

- **enabled-device 门禁**：`AgentEventFileWrite` 必须加入 `remoteAgentMessageRequiresEnabledDevice` 清单（`agent_remote_ws.go:1026-1040`，与 `FileList/FileRead/FileUpload/TransferUpload/TransferReceive` 同列）——该门禁在消息循环进 switch 前统一执行，未列入的类型默认放行；漏登记则禁用设备仍可写文件，与同族形成安全不对称。「四点齐」门禁测试（`agent_transfer_contract_test.go` 的 `TestAgentTransferGate` 先例）同步覆盖 file.write。
- **分发落点**：加入 `agent_remote_ws.go:836` 既有旧工具显式臂（`FileList/FileRead/...` 同列），沿用 `safeGo` panic 恢复；具体处理 case 落在 `dispatchDetailWithAI` → `handleAgentDetailMessageWithAI`（`agent_detail.go:44-59`）里新增分支——**不是**照 `file.upload` 的独立 case（`agent_remote_ws.go:777-781` 裸 go）模式。**不注册进 `agent_tool_registry`**（旧臂注释明令：显式臂事件严禁重复注册为注册表工具项——会被广告但永不走护栏分发）。

### 安全姿态（写死，不做开关）

- **不设设备级开关**：手机操作本机文件与手机交互终端（全 shell）同信任级，开关属安全表演；真实防线=TCC 守卫 + 路径守卫 + 大小上限 + 原子写。
- `error_code` 全集：`bad_request`（path/content 缺失早拒）`/ path_forbidden / bad_encoding / too_large / conflict / io_error / target_not_found`（父目录不存在）。文本错误照 `agentFileErrorPayload` 惯例不回显 URL/敏感路径以外信息。PhoneServer zod 侧的码枚举须对齐全集（含 `bad_request` 与 D4 的 `upload_url_not_allowed`）。

### 非目标（后续仓接/后续迭代）

- PhoneServer zod schema + 中转、手机 App 编辑 UI（功能在本仓交付后暂休眠，不影响现有链路）。
- 大文件写（>512KiB）与目录写、递归建目录、版本号锁（`expected_sha256` 已覆盖编辑竞态主场景）。

## D4：`file.upload` upload_url host 钉扎

`handleAgentFileUpload`（`agent_file_upload.go`）在发起 PUT 前对 `upload_url` 调用现成 `agentTransferHostAllowed`（`agent_transfer_common.go:182`，包级变量、测试可替换）：

- 不符 → 立即 `file.error`，`error_code="upload_url_not_allowed"`，报错文本不回显 URL（防签名泄漏，照互传先例）；
- 通过 → 行为完全不变（单飞槽、进度节流、ResponseHeaderTimeout 均不动）。

测试：钉扎拒绝（http/带端口/非 COS 域各一例）+ 放行不回归（测试替换 `agentTransferHostAllowed` 为 stub 或以 `myqcloud.com` 假域跑通）。

## 测试计划

| 层 | 内容 |
|---|---|
| 契约 | `agent_transfer_contract_test.go`：D2 optional 断言 + file.write 常量/契约条目/Emits 断言（照「四点齐」先例把 file.write 纳入本文件） |
| 单元 | `agent_file_write_test.go`：写已有（覆盖+mode 沿用）/新建/父目录缺失/512KiB+1 拒/base64 往返/expected_sha256 conflict/守卫拒绝路径各一例 |
| 单元 | `agent_file_upload_test.go` 增补：upload_url 钉扎三拒一放 |
| 单元 | `agent_ai_tool_ask_test.go`（或新文件）：bridge 五工具名 ↔ agentcore 点号名**映射表断言**（见 D1 三面命名表）；MCP 参数解析→payload 分发冒烟 |
| 契约 | `AgentEventFileError` Optional 含 `error_code` 断言（同批补登记的防漂移锚） |
| cmd | `cmd/mcp_test.go` 增补：stdio 名 ↔ 本桥名**映射表断言**（见 D1 三面命名表；注意 stdio 后三个无 peer_ 前缀是历史现状，断言按映射表写） |
| 回归 | 全仓 `go build ./...` + `go test ./app/http/services/... ./cmd/...`（注意预存 flake 清单：subprocess 超时族、`-race` 两枚基线红，见 memory，勿当回归） |

## 交付边界

- 全部为 additive 契约 + 客户端行为，不动 protocol_version，不破坏现有手机/服务端链路。
- 提交纪律：等用户明确说提交才 commit（CLAUDE.md）。
- 后续仓接项（不在本批）：PhoneServer `file.write` zod schema+中转与 `transfer.receive` direct_candidates zod 对齐核对、**`file.error` 的 `error_code` zod 对齐**、手机 App 编辑 UI、bridge 路径 peer 工具审计行。

## 复核发现的顺带事实（备查，不在本批修）

- `agent_protocol.go` 契约表无自动化「handler 实读键 vs 登记」一致性校验（D2 即漏网案例）；「四点齐」测试是人工点防。若后续要系统化，另立专项。
- peer 五工具在三个面（agentcore 桥/stdio 面/本批 MCP 桥）描述文本靠纪律+名集断言维持一致。
