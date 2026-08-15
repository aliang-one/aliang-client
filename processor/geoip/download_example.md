# GeoIP 自动下载功能说明

## 功能描述

当在配置文件中指定 GeoIP 数据库路径时，如果该文件不存在，系统会自动从以下 URL 下载：

```
https://git.io/GeoLite2-Country.mmdb
```

## 使用示例

### 配置文件示例

```json
{
  "routingRules": {
    "geoip": {
      "enabled": true,
      "databasePath": "/var/lib/geoip/GeoLite2-Country.mmdb",
      "chinaDirect": true
    }
  }
}
```

### 自动下载流程

1. **检查文件是否存在**
   - 系统检查 `/var/lib/geoip/GeoLite2-Country.mmdb` 是否存在

2. **自动创建目录**
   - 如果目录 `/var/lib/geoip/` 不存在，自动创建

3. **下载数据库**
   - 从 `https://git.io/GeoLite2-Country.mmdb` 下载数据库
   - 超时时间：5 分钟
   - 下载到临时文件：`/var/lib/geoip/GeoLite2-Country.mmdb.tmp`

4. **验证并重命名**
   - 下载完成后，重命名临时文件为最终文件
   - 记录下载的字节数

5. **加载数据库**
   - 打开并加载 GeoIP 数据库
   - 启用 GeoIP 服务

## 日志输出示例

```
[INFO] GeoIP database not found at /var/lib/geoip/GeoLite2-Country.mmdb, downloading from https://git.io/GeoLite2-Country.mmdb
[INFO] Downloaded 6421504 bytes
[INFO] GeoIP database downloaded successfully to /var/lib/geoip/GeoLite2-Country.mmdb
[INFO] GeoIP service initialized successfully (database: /var/lib/geoip/GeoLite2-Country.mmdb, chinaDirect: true)
```

## 错误处理

### 下载失败
如果下载失败（网络问题、URL 无效等），系统会返回错误并停止加载：

```
failed to download GeoIP database: failed to download from https://git.io/GeoLite2-Country.mmdb: ...
```

### 目录创建失败
如果无法创建目标目录（权限问题等）：

```
failed to create directory /var/lib/geoip: permission denied
```

### 数据库损坏
如果下载的文件损坏或格式错误：

```
failed to load GeoIP database from /var/lib/geoip/GeoLite2-Country.mmdb: ...
```

## 手动下载

如果需要手动下载数据库，可以使用以下命令：

```bash
# 创建目录
mkdir -p /var/lib/geoip

# 下载数据库
curl -L https://git.io/GeoLite2-Country.mmdb -o /var/lib/geoip/GeoLite2-Country.mmdb

# 或使用 wget
wget https://git.io/GeoLite2-Country.mmdb -O /var/lib/geoip/GeoLite2-Country.mmdb
```

## 代码实现

自动下载功能在 `processor/geoip/service.go` 中实现：

- `LoadDatabase(path string)` - 主入口，检查文件是否存在并触发下载
- `downloadDatabase(url, destPath string)` - 执行实际的下载操作

## 技术细节

1. **线程安全**: 使用 `sync.RWMutex` 确保并发安全
2. **原子操作**: 先下载到临时文件，然后原子重命名，避免部分下载
3. **超时控制**: 5 分钟下载超时，避免无限等待
4. **错误清理**: 下载失败时自动清理临时文件
5. **目录自动创建**: 使用 `os.MkdirAll` 递归创建目录
