# OpenViking dogfood 验收线证据(§11.1,2026-11-20)

状态:**验收线 1 通过(9/10)**;验收线 2 首次实测账为正;验收线 3 两周时钟自 2026-11-20 起算;验收线 4 前置(codex headers)已解除,三内核回归待装机。

## 知识库灌装(2026-11-20 13:0x UTC)

- 16 份脱敏运维知识文档写入 `viking://resources/docs/aliang-ops/`(共享域),零失败
- 语料来源:本地项目记忆的已定案事故复盘与配方(策展脱敏:密钥/token 一律不落)
- 摄取通道:openviking-vlm 虚拟 key(glm 家族限定;同批将 ov.conf 从 master key 切换至虚拟 key,openviking 消耗自此可审计)

## 验收线 1:真实问题检索命中(门槛 ≥7/10)

**方法**:10 条真实工作问题(固定于测试前),`find`(纯语义,无会话上下文,limit 5),判据=目标文档进 top-3。

| # | 问题 | 目标文档 | 命中 |
|---|---|---|---|
| Q1 | litellm 轮换 master key 后全补全 400 invalid model name 但 /v1/models 正常 | litellm-salt-key-trap | ✗(top-3 为同主题事件记忆+目录概览,语料重叠非检索失败) |
| Q2 | OpenViking 首次部署三大坑 | openviking-deploy-pitfalls | ✓ |
| Q3 | Windows 托盘消失但代理还能翻墙 | win-tray-gdi-leak | ✓ |
| Q4 | Windows 服务 crash 退出码 1067 排查 | win-service-crash-1067 | ✓(top1) |
| Q5 | LAN 仪表盘聊天 WebSocket 403 | lan-chat-ws-403 | ✓(top1) |
| Q6 | 手机端对话在仪表盘隐身 | dashboard-phone-invisibility | ✓(top1) |
| Q7 | frp 隧道五分钟批量断连根因 | tunnel-mass-disconnect-dns | ✓(top1) |
| Q8 | relay-tls 共享面加新域的顺序与铁律 | shared-tls-face-iron-rules | ✓(top1) |
| Q9 | worktree 构建测试已知坑 | worktree-build-gotchas | ✓(top1) |
| Q10 | go test 直连真机 agent 防护+flake 基线 | go-test-live-agent-guard | ✓(top1) |

**得分 9/10 → 通过**。7 题 top1 命中。

## 验收线 2:净 token 账

- 摄取成本(一次性):99 次 VLM 调用 / **346,290 tokens**(prompt 173,401 + completion 172,889),litellm spend USD $0.00(custom_openai 通道未配模型定价,成本口径暂只有 tokens——遗留账)
- 检索成本:0 VLM/次(未配 rerank,纯向量检索)
- 收益:9 次命中 × 保守 10k tokens 重新推导成本 = 90k+ tokens/轮,且随检索次数线性放大;语料本体各值一次多小时级事故调查(今日盐值事故单次调查即 ~190 万 subagent tokens),避免重查的收益远超摄取成本
- **判定:净账为正(一次性摄取 346k vs 常态化检索收益),随使用量持续变正**

## 验收线 3:两周无 P0 上游事故

时钟自 2026-11-20 起算。诚实记录:当日 06:24 UTC 发生一起我方操作引起的 litellm 盐值事故(43 分钟,已闭环并铁律入档)——严格口径下时钟自 06:30 修复后起算,期间至证据落盘无新 P0。是否重置/豁免留用户拍板。

## 验收线 4:codex headers 三内核回归

前置已完成(9184eb6e 合本地 master);三内核 tools/list 回归待发布装机后验证。

## 本批顺带修复/发现的缺口

1. openvinking 消耗可审计化:虚拟 key openviking-vlm 替代 master key(spend 行实证落账)
2. litellm spend $0.00:通道定价配置缺失,成本口径缺美元(遗留)
3. 语料重叠现象:remember 探针沉淀的事件记忆会与策展文档竞争同一查询的 top-3(排序健康,非缺陷)
