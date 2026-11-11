# 组网(mesh)收敛为 SSH 代理链——升级说明(2026-11-12)

本次版本把 mesh 从独立的帧协议/监听口收敛为「SSH 代理链」形态:mesh 流量复用对端 SSH 入口的握手、身份与加密。协议与配置结构均有不兼容变更,**已组网的多节点环境必须全节点同步升级、同步补配置**,否则组网整体失效。请通读下方三条 ⚠️ 警告,再按「升级七步」逐台执行。

## ⚠️ 新版 mesh 与旧版不互通(必须全节点同步升级)

新节点的入口只认 SSH 握手,旧节点的出口仍在写旧的 JSON 帧——两端对不上,连接静默失败,且不会有报错弹窗:

- LAN 载体:旧节点发 JSON 帧头,新节点嗅探已不识别 → 静默断连;
- piko 载体:旧节点写 JSON,新节点等 `SSH-` 握手 → 握手失败。

判定方法:`/api/mesh/status` 中 peer 长时间不可达、且对端已升级到新版,基本就是「只升了一半」。全部节点升完新版本后,再继续做下面的配置步骤。

## ⚠️ 组网现在依赖 SSH 入口(ssh_ingress)

mesh 出口流量现在走对端 SSH 入口(ssh_ingress)承载,不再有自己的监听口:

- mesh 开但 ssh_ingress 未配:升级后 mesh 不再生效(保存期硬拒/运行期降级——有意的 fail-closed);
- 56432 的 LAN 可达面改由 ssh_ingress 决定(原为 mesh 总开关),升级前请确认你想要的暴露面:
  - 开 ssh、关 mesh → 56432 绑 0.0.0.0,HTTP 代理面(socks5/CONNECT)随入口暴露到 LAN(会话租约仍是硬闸,无租约的连接进不来);
  - 开 mesh、关 ssh → mesh 静默失效,56432 回到回环。

⚠️ 注意第一条含义的反转:以前「只想暴露 56432 到 LAN」靠开 mesh 实现,现在要靠开 ssh_ingress;反过来,只想要 mesh(本地出口)而不要 LAN 暴露的用户,应保持 ssh_ingress 关闭——但那样 mesh 也一起失效。两者都要就两个都开。

## ⚠️ 未填齐密钥前 mesh: 规则静默失效

mesh 身份现在由三类密钥共同构成,缺一即该段组网不生效:

- 缺 `private_key` / `peers[].host_key` / `fallback.host_key` 的配置:保存期直接拒(前端正常保存路径会看到报错);
- 绕过保存的存量配置(手改配置文件后重启):编译期能力闸跳过该 peer 的规则(不报错、不弹窗),规则看起来存在但不生效。

排查方法:`/api/mesh/status` 新增的 `effective` 字段反映编译后的真实生效值——规则在但 `effective` 里没有,就是密钥没填齐,对照「升级七步」第 2/3/6 步补齐。

## 其它行为变更

- forward_enable 门禁收紧(nil=fail-closed,装配层恒注入活闭包);
- key_passphrase 会做 TrimSpace(首尾空格被改掉导致解不开钥——与 proxy.key_passphrase 同口径);
- ssh_ingress.port=0 时 mesh 入站随代理运行态启停;port>0 专用口默认只绑回环,lan_addrs 仍须填 56432;
- fallback.addr 语义变更:现指对端 SSH 入口 56432(旧值指向已删除的 mesh 监听口,不改则兜底必不通),且启用时 fallback.host_key 必填;
- `/api/mesh/status` 新增 effective 字段(生效值)与 peers[].host_key 回读;
- 不变项:DNS 代转发、失败策略(direct/deny/fallback:<id2>)语法零变化。

## 升级七步(逐台执行)

1. 开启 ssh_ingress(ssh_ingress.enable = true);
2. 生成本机 mesh 身份密钥对:私钥填 customer.mesh.private_key,公钥(带本机 device_id 尾注释)分发给每个对端,填入其对端的 ssh_ingress.mesh_authorized_keys;
3. 对每个 peer,把对端的 SSH 入口公钥填入本机 customer.mesh.peers[].host_key(来源=对端仪表盘「SSH 入口」卡片指纹);
4. 检查 peers[].lan_addrs 端口为 56432(不是专用口);
5. 跨网段/跨 NAT 对端确认 relay=true 且平台侧放行 <deviceID>.mesh + 下发 piko_proxy_url;
6. 离线兜底:fallback.addr 改填对端 IP:56432,并新增 fallback.host_key(旧值指向已删除的 mesh 监听口,不改则兜底必不通);
7. 确认前端版本已含组网密钥输入位(host_key×2/节点私钥),否则手改配置文件。

七步做完后,用 `/api/mesh/status` 核对:各 peer 可达、`effective` 字段包含预期出口规则,即升级完成。
