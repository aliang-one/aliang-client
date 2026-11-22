# 1c 附件规范: 手机面板 + server 加固 + provenance

**母文档**: `specs/005-event-listener/spec.md`(1a 全量契约不变)
**范围**: 手机 RN 面板(消费 server REST+notification 事件)、server SSRF 加固+webhook rate limiting、provenance `ai:<session_id>` server 端生成
**仓**: 手机 RN(`AliangVibeCodingPhone-ios27`)+ PhoneServer(`AliangPhoneServer`);agent 仓无改动

## RN-1: ListenerPanelScreen

- 新文件 `src/screens/operations/ListenerPanelScreen.tsx`,照 `EventStreamScreen.tsx` 范式(TopAppBar + SafeAreaWrapper + FlatList)
- 注册 `RootStackParamList` + `RootNavigator.tsx`
- 数据:`GET /api/listeners` + `GET /api/listeners/:id/events`(走 `src/api/client.ts` 的 fetch;新增 `src/api/listeners.ts` 域模块)
- 行:状态徽标(色映射同 web)+标题+最近触发相对时间+次数;error 行显示 error_note + 复活按钮(PATCH status active)
- 触发事件:行展开→`GET :id/events` 最新 20 条
- 操作:删除(confirm)

## RN-2: 入口

- SettingsScreen(Account tab)GlassPanel row 照 about-entry 惯例(testID `settings-listener-entry`)
- CommandCenter 快捷卡瓦片(照 ApprovalCenter 瓦片,testID `cc-listener-tile`),读 notification count 作徽标

## RN-3: 实时刷新

- server `createNotification` → `notification.created` 经 transport 流入 `handleTransportEvent`;listener 触发通知的 `type === 'notification.created'` 且 body 含 `listener_` 前缀 → toast(`useToastStore.show`)+面板列表刷新(如面板可见)
- 后台:server notification 已走 `useBackgroundNotifications` 既有管道,无需额外代码
- 不建新 socket;不加 tab

## RN-4: 触发时的 badge

- 通知到达时 badge count +1(复用 `approvalSlice` 的 unread 递增模式)
- 面板打开→badge 归零

## SV-1: SSRF 加固(http_poll URL 注册时校验)

- `listenerCreateSchema` http_poll params 增加 `url` refine:仅 http/https;hostname 解析后拒绝 loopback(127.0.0.0/8,::1)/link-local(169.254.0.0/16,fe80::/10)/RFC1918(10.0.0.0/8,172.16.0.0/12,192.168.0.0/16)
- 已注册的存量 URL 不追溯(1b 不做轮询侧二次校验)
- 测试:loopback URL 拒/私网 IP 拒/公网 URL 过

## SV-2: webhook rate limiting

- webhook 回调端点(唯一无 auth 面)加 per-IP 简易 429:内存 map IP→{count, window_start},窗口 60s,限 30 req/IP;超限 429 `webhook_rate_limited`
- 首次 token 校验失败计 1 次;合法回调不计(不阻碍正常系统)
- 测试:31 次不同 token 请求→第 31 次 429;合法回调不受影响

## SV-3: provenance `ai:<session_id>`

- `listenerCreateSchema` +`session_id` optional string(z.string().max(200));server `createListenerRow` 当 body 带 session_id 且设备面来源时:`created_by = 'ai:' + session_id`;否则照旧 `user:<deviceId>`
- 工具面:`listenerCreateViaCloud` +`session_id` optional 参数(agentcore 面=run.sessionID;stdio 面=缺省→server 落 user:<deviceId>)
- web 面板显示 created_by 时 `ai:` 前缀渲染为「AI 创建」(zh)

## 成功标准

1. 手机面板能创建/查看/复活/删除监听器;error 行可复活
2. 实时:触发→面板+badge+toast 三通道秒级可达
3. http_poll 注册 loopback/私网 URL → 422
4. webhook 31 次/分钟/IP → 第 31 次 429
5. AI 创建的监听器 created_by 带 `ai:<session_id>` 溯源
