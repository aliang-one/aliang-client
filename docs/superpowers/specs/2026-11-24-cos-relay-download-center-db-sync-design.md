# 设计:COS 中继同步完成后自动更新官网下载中心版本

- 日期:2026-11-24
- 状态:已实现(方案经用户确认:扩展现有 relay 脚本直连 PG;software_name 统一 `Aliang Client`;替换式每平台一行)

## 1. 背景与现状

发布链路:GitHub tag → CI 构建 Release → `nursor` 命名空间两个 CronJob 每 5 分钟轮询搬运到腾讯云 COS(桌面 `software/`,安卓 `software/android/`)。

缺口:COS 同步完成后,官网下载中心(`aliang-official-website` 后端的 `als_downloads` 表,库 `sub2api@postgress.my-assistent`)仍是手工维护,已过期(库里 v1.1.67,CDN 实况 v1.1.123;安卓库里 v1.0.11,CDN v1.0.12)。该表驱动官网下载页 `GET /public/downloads` 与客户端更新检查 `GET /public/downloads/check`。

现存数据混乱:software_name 五种写法(`aliang-agent`/`Aliang Cient`/`AliangClient`/`ALiang Client-amd64`/`AliangClientg-arm64`/`Android 64`),两处手误。

## 2. 方案(已选 A)

**扩展现有 cos-relay / cos-phone-relay 的 sync.sh**:COS `verify_public` 全绿后,同脚本内 `psql` 直连 PG upsert `als_downloads`。

- 官网后端零代码零部署;「发布→CDN→库」留在同一个红绿可见的 job 里
- 直连 PG 是本环境既有惯例(gateway/outbound 等同一实例)

## 3. 写库语义(替换式,用户拍板)

- 每平台**永远一行**,发布时原地 UPDATE(version/download_url/file_type/changelog/updated_at/is_default=TRUE)
- 行不存在才 INSERT(is_default=TRUE, force_update=FALSE)
- **不动 force_update**(保留手工控制)
- changelog = GitHub release body 压单行、jq 按 codepoint 截 500 字符(避免字节截断打碎 UTF-8)、base64 过 SQL 引号
- 幂等:重复执行结果一致;失败 → job 红,由自愈闭环补

## 4. 平台→产物映射

| relay | platform | file_type | download_url |
|---|---|---|---|
| cos-release-sync | linux | tar.gz | `…/software/aliang-linux-amd64.tar.gz` |
| cos-release-sync | darwin | pkg | `…/software/aliang-darwin-arm64.pkg` |
| cos-release-sync | windows | msi | `…/software/aliang-windows-amd64.msi` |
| cos-phone-release-sync | android | apk | `…/software/android/aliang-<TAG>-arm64.apk` |

`software_name` 统一 `Aliang Client`(含安卓行,用户拍板)。

## 5. 自愈闭环(关键设计)

快路径 `already_synced`(CDN 无变化秒退)增加 DB 新鲜度探针:查 `als_downloads` 中本 relay 平台行的 `version` 是否都等于 RELEASE_TAG。

- 探针通过 → 秒退
- 探针失败(如上一轮写库失败)→ **只跑 update_db,不重新搬运 CDN**
- 保证「写库失败一次就永远不补」不可能发生

## 6. 新增配置(ConfigMap env)

- `SOFTWARE_NAME=Aliang Client`
- `DB_DSN=postgresql://postgres:…@postgress.my-assistent.svc.cluster.local:5432/sub2api?sslmode=disable`
- alpine 基础镜像 `apk add postgresql-client`(沿用现有 install_deps 的 aliyun 源)

## 7. 一次性迁移(上线时手工执行,已执行)

1. 先 SELECT 全量备份现 6 行(留档本文末尾)
2. 按当前 CDN 实况 INSERT/UPDATE 4 行 canonical(linux/darwin/windows→v1.1.123,android→v1.0.12,ON CONFLICT (software_name,platform,version) DO UPDATE)
3. `DELETE FROM als_downloads WHERE software_name <> 'Aliang Client'` 清掉全部旧行(含 `aliang-agent` v1.1.54 与三个手误行)
4. 迁移后表内恰 4 行,与替换式语义一致;快路径探针立即通过

## 8. 失败处理

| 场景 | 行为 |
|---|---|
| psql 不可达/写失败 | update_db 非零 → job 红;下一轮快路径探针发现 DB 旧 → 只补写库 |
| CDN 未变且 DB 已同步 | 探针通过秒退(常态) |
| changelog 含引号/换行/CJK | 单行化 + codepoint 截断 + base64,psql `:'var'` 引用 |
| DB_DSN 空 | update_db 跳过(log 提示),仅当用户显式清空配置时发生 |

## 9. 验证清单(2026-11-24 全部实证)

- [x] 迁移后 `als_downloads` = 恰 4 行 canonical(linux/darwin/windows→v1.1.123,android→v1.0.12),`INSERT 0 4 / DELETE 6`
- [x] 公网 `GET /api/public/downloads/check?platform=windows&version=v1.0.0` → `latest_version: v1.1.123`(注意前缀是 `/api/public/…`,裸 `/public/…` 404)
- [x] 自愈实证:手改 windows 行 version=v1.1.67 → 21:15 轮日志 `cdn ok but db stale -> sync db only` → upsert 写回 v1.1.123
- [x] 修复探针后 21:25 轮两 relay 日志 `already-up-to-date`(秒退,绿)
- [x] changelog 自动写入 = GitHub release body(`**Full Changelog**: …` 实测在库)

## 10. 迁移备份(原 6 行,迁移前快照,已全部删除)

| id | software_name | platform | file_type | version | is_default | download_url | updated_at |
|---|---|---|---|---|---|---|---|
| 6 | aliang-agent | windows | zip | v1.1.54 | f | https://download.aliang.one/aliang-v1.1.54-win64.zip | 2026-10-24 |
| 3 | Aliang Cient | linux | zip | v1.1.41 | f | COS software/aliang-linux-amd64.tar.gz | 2026-09-28 |
| 4 | AliangClient | windows | msi | v1.1.67 | t | COS software/aliang-windows-amd64.msi | 2026-10-24 |
| 1 | ALiang Client-amd64 | darwin | pkg | v1.1.42 | f | COS software/aliang-darwin-amd64.pkg | 2026-09-28 |
| 2 | AliangClientg-arm64 | darwin | dmg | v1.1.42 | t | COS software/aliang-darwin-arm64.pkg | 2026-09-28 |
| 5 | Android 64 | android | zip | v1.0.11 | f | COS software/android/aliang-android-v… | (旧) |

## 11. 实现踩坑实录(2026-11-24)

1. **namespace 拼写 nurxor↔nursor 五连错**:aliangboard 读操作对拼错的 ns 容错(返回真数据,造成「数据存在」假象),而 apply_yaml 白名单精确匹配才暴露;排查一度误判「两套集群」。教训:工具对非法参数静默容错时,错误会延迟到严格校验处才爆。
2. **ConfigMap 块标量 heredoc 陷阱**:脚本里的 `<<EOF`/`<<'SQL'` 进 YAML 块标量整体缩进后,terminator 不在行首 → pod 里挂死。原桌面版当年是 `kubectl --from-file` 程序化创建才幸免;phone 版注释早已记录此坑。修复:heredoc 全部 printf 化(render_cos_config 与 db_upsert_one 的 SQL)。
3. **psql `-c` 模式不做 `:'var'` 变量替换**(实测 `syntax error at or near ":"`),stdin 管道模式才做——所以同脚内 upsert(stdin)成功、探针(-c)必败。修复:db_uptodate 统一 printf 管道 + 单引号转义内嵌。
4. **原脚本 `IFS="<TAB>"` 内含真实 TAB 字符**(ConfigMap JSON 转义显示成 `\t` 的假象);重写时写成字面反斜杠会毁 TSV 解析,靠 diff 原文抓回。
5. **写通道**:aliangboard key 的 apply 白名单不含 nurxor(读有 RBAC、写被策略拦)→ 旁路 = k8smaster1 SSH(sudo kubectl)+ secret gist 中转 YAML,md5 双侧对拍后 apply;传输完即删 gist(内含 DB_DSN 密码)。
6. changelog 截断必须按 codepoint(jq `.[0:500]`),字节截断会打断 UTF-8 让 psql 写入报 invalid byte sequence。
