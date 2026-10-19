# 手机消息送入终端会话（TUI）执行

> 2026-10-20 · 设计规格见 `docs/superpowers/specs/2026-10-20-tui-executed-phone-turns-design.md`

## 用户可见的变化

- 手机对某会话发消息时，若该会话的 Claude Code 终端窗口正开着且**空闲**，消息将直接
  送进那个终端执行：**终端与手机同步显示同一次对话**，上下文天然连续，不需要再各写
  一份历史。
- 这一回合的**工具审批在终端内完成**（手机不弹审批框）。手机端会标注该回合由终端执行。
- 该回合使用**终端自身的账号与模型**（即用户自己登录的那套），不是云端平台通道。
  这是有意的设计：走终端就是为了复用终端已有的上下文与环境。
- 终端正忙时行为不变：手机回合被拒绝（提示回合结束后重试），终端的回合不受干扰。
- 需要 Claude Code ≥ 2.1.224 且终端记录了消息 socket；不满足时自动回退原有方式
  （本机无头 `claude -p --resume` 执行），行为与之前完全一致。

## 为什么

在此之前，手机回合永远由 agent 自己 spawn 一个无头 `claude --print --resume` 子进程。
同一个会话若同时在终端里开着，两边各写各的：终端屏幕上看不到手机发起的回合，用户在
终端里接着聊时还要靠历史刷新才能对上。注入路径让「手机上发起、终端上执行」变成同一
次对话。

## 实现要点

| 环节 | 做法 |
|---|---|
| 检测 | `~/.claude/sessions/<pid>.json` 的 `status`(idle/busy) + pid 存活性 + 进程身份校验（防 pid 复用） |
| 路由 | `tuiTurnDecide` 七道门：provider 限 claude 系、必须有 native id、goal/fork 除外、TUI 存活且 idle、有 unix messaging socket、transcript 可定位 |
| 注入 | 经 TUI 的 peer inbox UDS 写 auth + user 帧（`ccPeerInject`）；内容 = 原始用户文本 + 附件清单 |
| 回传 | tail 同一份 transcript 合成 `ai.run.started` / `ai.delta` / `ai.thinking` / `ai.command` / `ai.run.progress` / `ai.done` |
| 停止 | `ai.stop` → 对 TUI pid 发一次 SIGINT（等效一次 Esc：中断回合、TUI 保持打开） |
| 兜底 | 全局上限 30 分钟；注入失败不回退 spawn（一次回合只走一条路） |

**已知限制：**

1. **流式粒度是块级，不是 token 级。** transcript 按块整行写入，所以手机端看到的
   delta 是「一段文字一次性到达」，而非逐字滚动。
2. **provider=auto 且存在空闲 claude 终端时，回合固定走终端。** 若用户显式选了
   codex，则不会走注入（provider 不匹配）。
3. **停止响应最多延迟约 3 秒**：注入后要观测 held 窗口（`ccPeerWatchHeld`），期间
   收到的 stop 会在窗口结束后才生效（状态仍会正确变为「已停止」）。
4. Windows 的 named pipe 注入尚未实现（v2），Windows 上自动回退原有方式。

## 回滚

整个功能可经 `tuiTurnDecide` 提前 `return nil` 一键关闭，回到今天的 spawn-only 行为。
无协议变更、无数据格式变更，回滚不需要迁移。
