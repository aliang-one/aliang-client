# 事件监听器 1c 实施计划(手机面板+server 加固+provenance)

**Spec**: `specs/005-event-listener/1c-spec.md`(RN-1~RN-4,SV-1~SV-3)
**仓**: 手机 RN(`AliangVibeCodingPhone-ios27`)+ PhoneServer(`AliangPhoneServer` wt/event-listener)
**方法**: 子代理逐任务+单轮合并评审(1c 面小,架构 1a 已证)

### Task RN: 手机面板+入口+实时刷新+badge
RN 仓:api/listeners.ts 域模块 + ListenerPanelScreen + SettingsScreen 入口 + CommandCenter 瓦片 + notification.created 分支(toast+badge+面板刷新) + i18n

### Task SV: SSRF+rate limiting+provenance
server 仓:http_poll URL 私网/loopback 拒 + webhook per-IP 429 + session_id provenance;三组测试

### Task V: 全量验证
RN tsc 无编译错误 / server 全量(2+1 预存)/ 手工断言走查
