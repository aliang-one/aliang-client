# 数据与隐私:知情披露与云端记忆删除设计(2026-11-27)

## 1. 背景与现状

2026-11-26 产品默认翻转后,OpenViking 跨设备记忆(`customer.openviking`,Phase2 spec)出厂即开:每个成功的平台驱动 AI 回合收尾,「用户消息 + 助手回复正文」自动上传至 `https://openviking.aliang.xin`(运营方服务器),服务端记忆抽取经过上游 LLM 供应商。设备互访、文件传输同批默认开启。

现有知情面缺口(2026-11-27 全链审查结论):

- OV 卡描述(`zh.js:765`)只说「会沉淀到账户」,未说明**自动上传时机**与**第三方模型处理**;
- 设置页页脚「隐私」链接是 `javascript:void(0)` 死链(`SettingsPage.vue:308`);
- 用户侧无任何删除通道:USER 角色 key 调不动 forget/set_acl(manage 级,Phase2 spec §9),管理面工具被 excludeTools 双保险裁掉(`agent_openviking.go:45-47`);
- Phase2 §13.2「卸载数据保留还是删除」未拍板,OV 卡删除按钮因此挂起(`OpenVikingSettings.vue:4` 注释)。

## 2. 目标

1. **被动常驻知情**:用户想看的时候看得到——零弹窗、零拦截、零危机感制造(用户 2026-11-27 拍板)。
2. **用户侧删除能力**:一键清除该账户全部云端记忆(全量粒度)。
3. **如实描述的简版隐私政策**:内嵌仪表盘,内容与技术事实一致。
4. 顺带拍板 Phase2 §13.2:关闭 OV 开关 = **保留**云端数据,删除只走显式按钮。

## 3. 非目标(YAGNI)

- **任何主动弹窗/首绑拦截/同意状态机**:用户拍板「不需要显式的提醒,避免制造危机感」。不新增 consent 落库状态。
- **手机端(RN)说明页**:用户拍板仅仪表盘。手机通道白名单另批次(沿 Phase2 先例)。
- **细粒度删除**(按会话/按时间):全量先行,粒度后置。
- **自动过期/保留期策略**:政策如实写「保留至删除或关闭」。
- **官网(sub2api-aliang)隐私政策页**:后续独立小活,本批仅仪表盘内嵌版。
- **设备互访/文件传输的行为变更**:本批只做知情披露,不翻默认值、不加门控。

## 4. 方案决策记录

| 决策点 | 拍板 | 依据 |
|---|---|---|
| 提示范围 | 「数据与隐私」总页一次交代三项默认开功能 | 用户拍板(推荐项) |
| 落地端 | 仅仪表盘 | 用户拍板 |
| 触达方式 | 零弹窗,常驻说明 + 卡片文案写实 | 用户拍板「避免制造危机感」 |
| 删除形态 | 全量清除按钮 + 二次确认 | 用户拍板(推荐项) |
| 卸载语义(Phase2 §13.2) | 关闭开关保留云端数据;删除只走显式按钮 | 与「全量清除按钮」选择一致;误触关开关不丢数据 |
| BYO 删除 | 不支持、不代删:**BYO 激活态**(enabled 且 mode=byo)按钮隐藏 + 后端 400 双保险;关闭态不设注册门槛 | ROOT key 不在平台手里,无法也不该代删用户自有部署;平台 purge 永远只触碰平台托管数据 |
| 删除链路 | agent → 云端铸 key 同族管理端点,ROOT key 服务端持有 | 沿 credential 端点(Phase2 §7.1)成熟模式 |

## 5. 架构总览

```
仪表盘 SettingsPage 页脚「隐私」死链 → 「数据与隐私」弹层(常驻,三段+政策全文)
仪表盘 OpenViking 卡「删除云端记忆」按钮(二次确认)
  → POST /api/agent/openviking/purge(alianggate agent,既有 agent API 鉴权)
    → BYO 激活态:400 双保险;关闭态/托管态:放行(注册门槛已废除)
    → POST <APIBaseURL>/api/agent/openviking/purge(云端,设备 JWT+设备 ID 头,
       与 credential 端点同契约、同两级凭据回落)
      → PhoneServer 鉴权 → 解析平台账号 → openviking_credentials 表取 OV user_id
      → 持 ROOT key 调 OpenViking 管理面,清除该 user 全部记忆
      → 200 {purged:true} / 失败状态码+原因原样透传
  → 仪表盘失败 dialog 显原因,可重试
```

## 6. 详细设计

### 6.1 「数据与隐私」弹层(alianggate 仪表盘)

- **挂点**:激活 `SettingsPage.vue:308` 死链,`@click` 打开弹层;呈现范式照 `openTutorialDocs`(实施时对齐既有弹层/抽屉组件,不新造形态)。
- **新建组件** `DataPrivacyDialog.vue`(settings/ 目录,范式照 OpenVikingSettings 的组件纪律),内容三段:
  1. **对话记忆(OpenViking)**:说明「回合成功后自动上传用户消息与助手回复正文至云端记忆库;记忆提取由模型服务处理(含第三方 LLM 供应商);不收集工具调用记录与内部推理;可在 OpenViking 卡关闭」。
  2. **设备互访 / 文件传输**:出厂开启,数据在用户自己的设备之间流转,开关位置指引(设备互访卡/文件传输设置)。
  3. **删除与政策**:删除入口指引(OpenViking 卡按钮;BYO 模式请在你的部署中自行管理);简版隐私政策全文内嵌弹层底部(§6.5)。
- i18n:`zh.js`/`en.js` 双语同批,文案口径照 `agent_integrations_*` 家族(全角标点/「」/「你」)。

### 6.2 OV 卡文案改写实

`openViking_description`(zh+en)改为如实描述:**「默认开启(托管模式):每轮对话的用户消息与助手回复会在回合成功后自动上传至云端记忆库,跨设备检索共享;记忆提取由模型服务处理。选择「关闭」并保存即停止上传并移除托管条目(云端已存数据保留,可用「删除云端记忆」清除)。」**

### 6.3 删除按钮 + agent purge 端点(alianggate)

- **OpenVikingSettings.vue** 状态行旁新增「删除云端记忆」按钮:
  - `form.mode==='byo'`(BYO 激活态):按钮不渲染,显示提示「BYO 模式请在你的部署中自行管理数据」;
  - **其余形态一律可用,含关闭态**(`mode==='off'`)——关闭后按钮是唯一删除通道(§4 卸载语义),不存在「未注册禁用」规则(off 态下注册表条目已被 cleanup 清除,registered 恒 false,按 registered 门禁会把按钮锁死在唯一需要它的时刻——评审修);
  - 点击 → 二次确认(确认文案写明「将删除该账户在**所有设备**的云端记忆,不可恢复」)→ 调 purge → 成功绿态提示 + `loadStatus()` 刷新;失败 `showErrorDialog` 带原因。
- **agent 侧新端点** `POST /api/agent/openviking/purge`(`routes.go` 照 status/apply 注册):
  - 前置闸**只保留一条**:`EffectiveOpenVikingEnabled() && EffectiveOpenVikingMode()==byo`(BYO 激活态)→ 400 `byo_not_supported`。关闭态(enabled=false)不设任何门槛:purge 触碰的永远只是**平台托管**数据,历史 BYO 用户若曾用托管,遗留数据也由此通道清除。`byo_detected`(managed 配置但注册表保留用户自有条目,`agent_openviking.go:184-186`)**不**触发此闸——该路径与用户自有部署无关。不设 `not_registered` 409:前端不查 registered、后端不设注册门槛,从未铸 key 的账号由云端幂等 200 兜底(§6.4)。
  - 云端调用:新建 `openvikingPurgeRemote()`,契约**逐字段照 `fetchOpenVikingCredential`**(`agent_openviking.go:103-152`):URL=`<APIBaseURL>/api/agent/openviking/purge`;凭据两级回落(转发槽 `UserAccessToken()` → `effectiveAgentRegisterAuthHeader`);必带 `X-Aliang-Device-ID`;超时 10s。错误透传规则:云端返回非 200 → 原样透传状态码与原因;**云端调用超时(无远端状态码)→ 本地合成 504**。
  - 失败只返回错误,不影响任何 run/ boot 流程;无重试队列(YAGNI,按钮可手动重试)。

### 6.4 云端 purge 端点(PhoneServer 仓,~100 行)

- `POST /api/agent/openviking/purge`,鉴权中间件与 credential 端点同款(`requireAgentIdentity`),解析设备 → 平台账号。
- 流程:查 `openviking_credentials` 表取该账号 OV user_id 与 key 记录 → 持 ROOT key(secret,沿 Phase2 §7.1 存放)调 OpenViking 管理面删除该 user 全部记忆 → 200 `{purged:true}`。
- 语义:**幂等**(重复调用成功);账号无 OV 记录(从未铸 key)→ 200 `{purged:true}`(无数据即删成,避免前端分支);OV 不可达 → 502 + 原因透传。
- ⚠️ **实施前置侦察项**:OpenViking server(v0.5.x,AGPL)「删除 user 全部记忆」的具体调用形态未探明——候选:Admin REST API 直删 / ROOT key 经 MCP `tools/call` 逐资源 forget。实施第一步先在集群内实测定契约,本 spec 不拍死内部 API 形态,只锁定上表对前端的契约(200/502/幂等)。

### 6.5 简版隐私政策(内嵌文本)

弹层底部内嵌,如实描述,草稿要点(正式文本实施时按此骨架落 i18n):

> 本平台默认开启以下功能,数据流向如下,你随时可以在设置中关闭:
> 1. **对话记忆**:每轮对话的用户消息与助手回复,在回合成功后自动上传至运营方云端记忆库(openviking.aliang.xin),用于跨设备记忆检索;记忆提取处理会经过运营方配置的模型服务(含第三方大模型供应商)。工具调用记录与模型内部推理不在上传范围。关闭本功能即停止上传,**已存数据保留至你主动删除**(关闭不会删除);若运营方终止本服务,将按届时公布的迁移/删除政策处理。
> 2. **设备互访 / 文件传输**:默认开启,用于你的设备之间互访与传输;数据仅在你自己的设备之间流转,不经第三方。
> 3. **删除**:你可在「设置 → OpenViking 卡 → 删除云端记忆」一键清除全部云端记忆,该操作不可恢复且作用于你账户的所有设备。
> 正式法律文本以官网发布为准;商用前经法律审查。

## 7. 接口契约

| 接口 | 方法 | 鉴权 | 成功 | 失败 |
|---|---|---|---|---|
| `/api/agent/openviking/purge`(agent) | POST | agent API 既有中间件 | 200 `{purged:true}` | 400 `byo_not_supported`(仅 BYO 激活态)/ 502 云端原因透传 / 504 本地合成(云端调用超时,无远端状态码;其余非 200 原样透传) |
| `/api/agent/openviking/purge`(云端) | POST | requireAgentIdentity + 设备 ID 头 | 200 `{purged:true}`(幂等,含从未铸 key) | 502 OV 不可达+原因 / 401/400 沿中间件语义 |

## 8. 安全考量

- ROOT key 仍只在 PhoneServer secret;agent 侧 purge 不经手任何 key。
- purge 是破坏性账号级操作:二次确认 + 文案写明「所有设备、不可恢复」;agent 与云端双层鉴权;BYO 双保险拒绝。
- 云端调用沿「headers 值永不进日志」红线;错误透传只透状态码与原因文本,不透 key/响应体敏感字段。
- 删除按钮不新增任何数据读取面——不回显任何记忆内容,纯清除。

## 9. 生效链

1. PhoneServer purge 端点先行(或同批)部署——老 agent 不调用新端点,无兼容风险;
2. alianggate 出包(弹层 + 按钮 + agent 端点)装机;
3. 时序倒置(新 agent 打旧云端,端点不存在)→ purge 返回 404,dialog 透传原因引导升级云端,无静默失败路径;
4. 官网政策页(sub2api-aliang)独立小活后置;正式法律文本挂 TODOS,商用前律师定稿。

## 10. 验收标准

1. 页脚「隐私」链接可点,弹层三段内容 + 政策全文,zh/en 双语完整;
2. OV 卡描述与实际行为一致(自动上传时机 + 模型服务处理 + 关闭语义);
3. BYO 激活态:无按钮、有提示;agent 直调 purge 返回 400(不发云端);`byo_detected` 不触发 400(正常发云端,只清平台数据);
4. 关闭态(enabled=false):按钮可用,agent 放行发云端;
5. managed + 已注册:确认流 → purge 成功 → 云端该 user 记忆清空(集群内实测验证)+ 重复 purge 仍 200;从未铸 key 的全新设备直接 purge → 云端幂等 200;
6. 云端不可达:仪表盘 dialog 显原因,OV 功能面(capture/拨测)不受影响;
7. 关闭 OV 开关:云端数据保留(实测),关态下删除按钮仍可用且能成功清除;
8. 单测:agent handler 三分支(BYO 激活态 400/关闭态放行/透传)+ 云端客户端契约(头/超时 504/非 200 透传);Vue 组件按钮三态(隐藏/关闭态可用/确认流)+ 弹层渲染;PhoneServer 端点鉴权/幂等/502(注意该仓 3 个预存 flaky 基线,勿误判)。

## 11. 风险与缓解

| 风险 | 缓解 |
|---|---|
| OV 删除 API 形态未探明 | §6.4 实施前置侦察项;前端契约不依赖内部形态 |
| 账号级删除误触(双设备) | 二次确认 + 「所有设备/不可恢复」文案;关闭≠删除(§4) |
| 云端未部署时按钮报错 | 502/404 透传原因,dialog 引导;PhoneServer 同批先行 |
| 用户误以为关闭即删除 | 卡描述与弹层双处写明「关闭保留,删除走按钮」 |

## 12. 未决问题

1. OV server 删除调用形态(§6.4 侦察项,实施第一步定)。
2. 官网政策页与正式法律文本:时点与律师审查(挂 TODOS,非本批)。
