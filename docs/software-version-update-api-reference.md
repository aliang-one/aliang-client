# 软件版本更新 API 文档

> 本文档基于当前仓库实现整理，覆盖软件下载发布、版本检查、客户端接入方式，以及当前实现中的关键约束。

## 1. 概览

当前项目的软件版本更新能力是基于“下载中心”实现的，而不是 Sparkle、App Store、Electron Auto Updater 这类平台原生升级方案。

整体流程如下：

1. 管理员在下载中心发布一个新版本，写入下载记录。
2. 后端把每个版本作为一条 `als_downloads` 记录保存。
3. 客户端启动时、定时轮询时，调用公开检查接口提交自己的平台和当前版本。
4. 后端比较客户端版本和数据库中的最高版本，返回是否需要更新、是否强制更新、下载地址和更新说明。

## 2. 基础信息

### 2.1 前端代理地址

站内页面、桌面客户端如果接的是网站层，通常访问 Next.js 暴露的接口：

- `GET /api/public/downloads`
- `GET /api/public/downloads/check`
- `GET /api/admin/download-center`
- `POST /api/admin/download-center`
- `GET /api/admin/download-center/{id}`
- `PUT /api/admin/download-center/{id}`
- `DELETE /api/admin/download-center/{id}`

### 2.2 后端真实地址

Next.js 会把请求转发到后端服务，目标地址由环境变量控制：

- `API_BASE_URL`
- `NEXT_PUBLIC_API_BASE_URL`

例如：

```txt
https://your-frontend.example.com/api/public/downloads/check
```

会转发到：

```txt
https://your-backend.example.com/public/downloads/check
```

### 2.3 数据来源

版本发布信息来自数据库表 `als_downloads`，核心字段如下：

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int64 | 记录 ID |
| `software_name` | string | 软件名/软件标识 |
| `platform` | string | 平台标识 |
| `file_type` | string | 安装包类型，如 `dmg`、`exe`、`zip` |
| `download_url` | string | 下载地址 |
| `version` | string | 版本号 |
| `force_update` | bool | 是否强制更新 |
| `changelog` | string | 更新说明 |
| `is_default` | bool | 是否为默认下载项 |
| `created_at` | string | 创建时间 |
| `updated_at` | string | 更新时间 |

## 3. 版本规则

### 3.1 当前支持的版本格式

当前实现要求版本号必须满足：

```txt
vMAJOR.MINOR.PATCH
```

例如：

- `v1.0.0`
- `v1.23.123`
- `v10.200.3000`

### 3.2 当前格式约束

- 必须带 `v` 前缀
- 三段都必须是纯数字
- 每段支持多位数字
- 不支持 `1.2.3` 这种无 `v` 前缀格式
- 不支持 `v1.2`、`v1.2.3.4`
- 不支持 `v1.2.3-beta`

### 3.3 版本比较规则

后端会把版本拆成 `major.minor.patch` 三段数字进行比较。

例如：

- `v1.23.123` > `v1.10.2`
- `v2.0.0` > `v1.999.999`
- `v1.2.10` > `v1.2.3`

## 4. 平台标识

当前实现中的平台值以代码实际行为为准：

| 平台 | 取值 |
|------|------|
| macOS | `darwin` |
| Windows | `windows` |
| Linux | `linux` |

注意：

- macOS 客户端应传 `darwin`
- 当前实现不是 `macos`

## 5. 公开接口

## 5.1 获取下载列表

用于展示官网软件下载页、平台下载入口等。

### 请求

```http
GET /api/public/downloads
```

可选查询参数：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `platform` | string | 否 | 按平台筛选，可传 `darwin`、`windows`、`linux` |

### 示例

```http
GET /api/public/downloads?platform=darwin
```

### 响应

```json
{
  "downloads": [
    {
      "id": 1,
      "software_name": "aliang-helper",
      "platform": "darwin",
      "file_type": "dmg",
      "download_url": "https://example.com/downloads/aliang-helper-v1.23.123.dmg",
      "version": "v1.23.123",
      "force_update": false,
      "changelog": "Bug fixes and performance improvements",
      "is_default": true,
      "created_at": "2026-04-07T10:00:00Z",
      "updated_at": "2026-04-07T10:00:00Z"
    }
  ]
}
```

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `downloads` | array | 下载列表 |
| `downloads[].is_default` | bool | 官网列表里用于挑选默认下载项 |

### 说明

- 该接口只是列出下载项
- `is_default` 主要影响展示层默认按钮选择
- 该接口不负责判断是否需要更新

## 5.2 检查版本更新

这是客户端检查新版本的核心接口。

### 请求

```http
GET /api/public/downloads/check
```

查询参数：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `platform` | string | 是 | 客户端平台，传 `darwin`、`windows`、`linux` |
| `version` | string | 是 | 客户端当前版本，例如 `v1.23.123` |
| `software` | string | 否 | 软件标识/软件名，用于区分多个软件 |

### macOS 客户端示例

```http
GET /api/public/downloads/check?platform=darwin&software=aliang-helper&version=v1.23.123
```

### curl 示例

```bash
curl "http://localhost:3000/api/public/downloads/check?platform=darwin&software=aliang-helper&version=v1.23.123"
```

### 响应示例 1：有更新，非强制

```json
{
  "software_name": "aliang-helper",
  "platform": "darwin",
  "current_version": "v1.23.123",
  "latest_version": "v1.24.0",
  "download_url": "https://example.com/downloads/aliang-helper-v1.24.0.dmg",
  "file_type": "dmg",
  "force_update": false,
  "needs_update": true,
  "changelog": "Improved onboarding and fixed startup crash"
}
```

### 响应示例 2：有更新，强制

```json
{
  "software_name": "aliang-helper",
  "platform": "darwin",
  "current_version": "v1.23.123",
  "latest_version": "v2.0.0",
  "download_url": "https://example.com/downloads/aliang-helper-v2.0.0.dmg",
  "file_type": "dmg",
  "force_update": true,
  "needs_update": true,
  "changelog": "Security patch, upgrade required"
}
```

### 响应示例 3：已经是最新版本

```json
{
  "software_name": "aliang-helper",
  "platform": "darwin",
  "current_version": "v1.23.123",
  "latest_version": "v1.23.123",
  "download_url": "https://example.com/downloads/aliang-helper-v1.23.123.dmg",
  "file_type": "dmg",
  "force_update": false,
  "needs_update": false,
  "changelog": "Current stable release"
}
```

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `software_name` | string | 软件名 |
| `platform` | string | 平台标识 |
| `current_version` | string | 客户端当前版本 |
| `latest_version` | string | 后端判定的最新版本 |
| `download_url` | string | 最新版本下载地址 |
| `file_type` | string | 最新版本文件类型 |
| `force_update` | bool | 是否强制更新 |
| `needs_update` | bool | 是否需要更新 |
| `changelog` | string | 更新说明 |

### 判定规则

后端判定逻辑如下：

1. 按 `platform` 查询下载记录
2. 如果传了 `software`，继续按 `software_name` 过滤
3. 找出匹配记录中的最高版本
4. 与客户端传入的 `version` 比较
5. 返回是否需要更新

### `force_update` 的含义

`force_update` 只会在以下条件同时满足时为 `true`：

1. 客户端确实不是最新版本
2. 最新版本那条发布记录被标记为了强制更新

如果客户端已经是最新版本，则返回：

```json
{
  "force_update": false,
  "needs_update": false
}
```

### 注意事项

- `is_default` 不参与版本检查时的“最新版本”选择
- 版本检查看的是最高版本号，不是默认下载项
- 如果同一平台下有多个软件，建议客户端始终传 `software`

### 错误响应

#### 400 参数错误

```json
{
  "error": "platform and version are required"
}
```

或：

```json
{
  "error": "invalid version format, expected vMAJOR.MINOR.PATCH with numeric segments"
}
```

#### 404 未找到匹配版本

```json
{
  "error": "no downloads found for the given criteria"
}
```

## 6. 管理接口

管理接口用于发布新版本、修改版本、删除旧版本。

这些接口都需要管理员认证，请求头中带：

```http
Authorization: Bearer <session_token>
```

## 6.1 获取下载列表

```http
GET /api/admin/download-center
```

响应示例：

```json
{
  "downloads": [
    {
      "id": 1,
      "software_name": "aliang-helper",
      "platform": "darwin",
      "file_type": "dmg",
      "download_url": "https://example.com/downloads/aliang-helper-v1.23.123.dmg",
      "version": "v1.23.123",
      "force_update": false,
      "changelog": "Bug fixes",
      "is_default": true,
      "created_at": "2026-04-07T10:00:00Z",
      "updated_at": "2026-04-07T10:00:00Z"
    }
  ]
}
```

## 6.2 创建下载项

```http
POST /api/admin/download-center
Content-Type: application/json
Authorization: Bearer <session_token>
```

请求体示例：

```json
{
  "software_name": "aliang-helper",
  "platform": "darwin",
  "file_type": "dmg",
  "download_url": "https://example.com/downloads/aliang-helper-v1.23.123.dmg",
  "version": "v1.23.123",
  "force_update": false,
  "changelog": "Bug fixes and performance improvements",
  "is_default": true
}
```

说明：

- 如果 `is_default=true`，同一 `software_name + platform` 下其他默认项会被自动取消
- `version` 必须符合当前版本规则

## 6.3 获取单条下载项

```http
GET /api/admin/download-center/{id}
Authorization: Bearer <session_token>
```

## 6.4 更新下载项

```http
PUT /api/admin/download-center/{id}
Content-Type: application/json
Authorization: Bearer <session_token>
```

请求体格式与创建接口一致。

## 6.5 删除下载项

```http
DELETE /api/admin/download-center/{id}
Authorization: Bearer <session_token>
```

响应示例：

```json
{
  "deleted": true
}
```

## 7. 客户端接入建议

## 7.1 macOS 客户端建议

建议客户端在以下时机调用检查接口：

1. 应用启动完成后检查一次
2. 用户手动点击“检查更新”时检查一次
3. 长时间运行的客户端可定时轮询，例如每隔数小时检查一次

推荐请求：

```http
GET /api/public/downloads/check?platform=darwin&software=<your-software-name>&version=<current-version>
```

例如：

```http
GET /api/public/downloads/check?platform=darwin&software=aliang-helper&version=v1.23.123
```

## 7.2 客户端处理建议

### `needs_update=false`

- 不提示更新
- 或仅在“关于”页面显示“已是最新版本”

### `needs_update=true` 且 `force_update=false`

- 弹出普通升级提示
- 展示 changelog
- 提供“立即下载”或“稍后提醒”

### `needs_update=true` 且 `force_update=true`

- 弹出强制升级提示
- 阻止用户继续使用旧版本，或限制关键功能
- 跳转到 `download_url`

## 8. 当前实现的限制

在接入客户端前，建议注意以下事实：

1. 当前版本格式不是完整语义化版本实现，只支持 `vMAJOR.MINOR.PATCH`
2. 不支持预发布标签，如 `-beta`、`-rc1`
3. 不支持构建元数据，如 `+build.7`
4. `software` 参数虽然是可选的，但如果你的系统有多个软件，强烈建议必传
5. `is_default` 只影响下载列表默认项，不决定版本检查结果

## 9. 推荐测试用例

建议在联调时验证以下场景：

1. 当前版本等于最新版本，返回 `needs_update=false`
2. 当前版本低于最新版本，返回 `needs_update=true`
3. 最新版本开启强制更新时，返回 `force_update=true`
4. 多位数字版本号比较正确，例如 `v1.23.123 > v1.10.2`
5. macOS 平台传 `darwin` 可正常命中记录
6. 不带 `v` 前缀时返回 400

## 10. 接口清单速览

| 方法 | 路径 | 认证 | 用途 |
|------|------|------|------|
| GET | `/api/public/downloads` | 否 | 获取下载列表 |
| GET | `/api/public/downloads/check` | 否 | 检查是否有新版本 |
| GET | `/api/admin/download-center` | 是 | 获取后台下载列表 |
| POST | `/api/admin/download-center` | 是 | 发布新版本 |
| GET | `/api/admin/download-center/{id}` | 是 | 获取单条版本记录 |
| PUT | `/api/admin/download-center/{id}` | 是 | 更新版本记录 |
| DELETE | `/api/admin/download-center/{id}` | 是 | 删除版本记录 |
