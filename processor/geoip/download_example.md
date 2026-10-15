# GeoIP 数据库自动下载说明

## 功能描述

启动时（start / tray / core 三条启动路径共用同一编排）装载 GeoIP 国家数据库：本地已有则直接加载；本地缺失则自动在后台从用户 COS 分发地址下载（不阻塞启动），完成后 geoip 规则（`geoip:CN`、`GEOIP,CN,proxy` 等）自动生效。

默认下载地址：

```
https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb
```

环境变量 `ALIANG_GEOIP_DOWNLOAD_URL` 可覆盖下载地址（指向自建镜像或内网文件服务等）。

数据库落点由 `DefaultDatabasePath()` 解析：本地数据目录下的 `geoip/GeoLite2-Country.mmdb`（默认 `~/.aliang/geoip/GeoLite2-Country.mmdb`，支持 `~` 展开；daemon 模式解析到对应 state 目录下的 `geoip/`）。

## 下载流程（`EnsureDownloaded`，锁外执行）

1. **检查文件是否存在**——已存在则零网络直接返回（无锁快路径）
2. **并发 single-flight**——并发调用共享 `.tmp` 临时文件会互相碰撞，互斥锁内 double-check 后再查一次文件是否已被其他调用者下载完成
3. **自动创建目录**——目标目录不存在时 `os.MkdirAll` 递归创建
4. **下载数据库**——5 分钟超时；先写入 `.tmp` 临时文件，成功后原子重命名为最终文件；失败自动清理临时文件

装载（`LoadLocal`）只做本地 IO：打开数据库、原子替换内存 reader 并启用服务。下载绝不持有服务锁——下载进行期间查询立即返回未命中，装载完成后立即可查。

## 日志输出示例

```
[INFO] GeoIP database not found, downloading in background: https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb
[INFO] GeoIP database not found at ~/.aliang/geoip/GeoLite2-Country.mmdb, downloading from https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb
[INFO] GeoIP database downloaded successfully to ~/.aliang/geoip/GeoLite2-Country.mmdb
[INFO] GeoIP database ready; geoip rules now active
```

配置引用了 geoip 条目而数据库仍在后台下载时，会追加一条提醒：

```
[INFO] geoip 数据库后台下载中，geoip 规则就绪后自动生效
```

## 错误处理

下载或装载失败**不阻断启动**：geoip 规则本次运行不生效，仅记录日志，下次启动自动重试：

```
[WARN] GeoIP database background download failed (will retry next startup): failed to download GeoIP database: failed to download from https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb: ...
[WARN] GeoIP database load failed after download: failed to load GeoIP database from ~/.aliang/geoip/GeoLite2-Country.mmdb: ...
```

## 手动下载 / 刷新

数据库不做自动刷新，建议每月刷新一次——删除本地文件后重启（触发重新下载），或直接覆盖该文件：

```bash
# 以默认路径 ~/.aliang/geoip/ 为例（daemon 模式为对应 state 目录下 geoip/）
curl -L https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb \
  -o ~/.aliang/geoip/GeoLite2-Country.mmdb

# 或使用 wget
wget https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb \
  -O ~/.aliang/geoip/GeoLite2-Country.mmdb
```

上游镜像源（官方 license key 每周构建，可用于刷新 COS 副本）：
`https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-Country.mmdb`

## 代码实现

自动下载与装载在 `processor/geoip/service.go` 中实现：

- `EnsureLoadedAsync()` - 启动编排入口：本地优先同步装载，缺失转后台异步（start / tray / core 三路径共用）
- `EnsureDownloaded(path)` - 确保本地存在数据库文件，缺失则下载（锁外、single-flight）
- `LoadLocal(path)` - 仅加载本地数据库并原子替换 reader（缺失即错、不下载）
- `downloadDatabase(url, destPath)` - 实际下载（`.tmp` 临时文件 + 原子重命名）

## 技术细节

1. **锁纪律**：下载在服务锁外执行（独立的下载互斥），装载仅持锁做本地 IO 与 reader 原子替换
2. **原子落盘**：先写临时文件再重命名，避免半截文件被当成有效数据库
3. **超时控制**：5 分钟下载超时，避免无限等待
4. **错误清理**：下载失败自动清理临时文件
5. **目录自动创建**：`os.MkdirAll` 递归创建
