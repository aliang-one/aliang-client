# 前端 API 文档 — 健康检查、下载管理、配置中心

> 本文档涵盖前端（Next.js）暴露的三类 API：**健康检查**、**软件下载管理**、**配置中心**。
> 所有接口均由 Next.js Route Handler 实现，大部分透传到后端微服务。

## 通用约定

| 项目 | 说明 |
|------|------|
| **Base URL** | `http://<host>:3000` |
| **Content-Type** | `application/json` |
| **认证** | 管理 API 需在请求头携带 `Authorization`（透传至后端校验） |
| **后端地址** | 通过环境变量 `API_BASE_URL` / `NEXT_PUBLIC_API_BASE_URL` 配置 |
| **错误响应** | 所有接口统一格式：`{ "error": "<描述>" }`，HTTP 状态码见各接口 |

---

## 一、健康检查

### 1.1 服务健康评分

后端微服务的综合健康状态，由前端 **2 秒定时轮询** 并缓存，客户端访问时直接返回缓存值。

```
GET /api/health
```

**认证**：无需认证（公开接口）

**说明**：前端每 2 秒从后端 `/api/v1/admin/ops/dashboard/snapshot-v2` 获取 `health_score` 并缓存到内存。此接口返回的是缓存的快照，响应时间极快。

**响应示例**：

```json
{
  "health_score": 98,
  "updated_at": "2025-03-24T10:00:00.000Z",
  "error": null
}
```

**响应字段**：

| 字段 | 类型 | 说明 |
|------|------|------|
| `health_score` | int \| null | 后端健康评分（0-100），首次请求或后端不可用时为 `null` |
| `updated_at` | string \| null | 最后一次成功更新的时间（ISO8601），尚未更新过时为 `null` |
| `error` | string \| null | 最近一次轮询的错误信息，正常时为 `null` |

**状态码**：始终返回 200（错误信息通过 `error` 字段传递）

### 1.2 运行时配置

返回前端运行时的后端 API 地址配置，可用于诊断前端是否正确连接到后端。

```
GET /api/runtime-config
```

**认证**：无需认证（公开接口）

**响应示例**：

```json
{
  "apiBaseUrl": "http://aliang-website-backend:8081"
}
```

**响应字段**：

| 字段 | 类型 | 说明 |
|------|------|------|
| `apiBaseUrl` | string | 当前生效的后端 API 地址 |
| `error` | string | 仅在配置缺失时出现 |

**错误响应**（500）：

```json
{
  "error": "API_BASE_URL or NEXT_PUBLIC_API_BASE_URL is not set"
}
```

---

## 二、软件下载管理

### 2.1 公开接口

#### 获取下载列表

获取所有可用的软件下载项，可选按平台筛选。

```
GET /api/public/downloads
```

**认证**：无需认证

**Query 参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `platform` | string | 否 | 按平台筛选，如 `windows`、`macos`、`linux` |

**透传目标**：`GET <backend>/public/downloads`

**响应示例**：

```json
[
  {
    "id": 1,
    "software_name": "aliang-helper",
    "platform": "windows",
    "file_type": "exe",
    "download_url": "https://example.com/download/aliang-helper-1.2.0.exe",
    "version": "v1.2.0",
    "force_update": false,
    "changelog": "Bug fixes and performance improvements",
    "is_default": true,
    "created_at": "2025-03-20T10:00:00Z",
    "updated_at": "2025-03-20T10:00:00Z"
  }
]
```

#### 检查版本更新

检查指定软件是否有新版本可用。

```
GET /api/public/downloads/check
```

**认证**：无需认证

**Query 参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `platform` | string | 是 | 客户端平台，如 `windows`、`macos`、`linux` |
| `version` | string | 是 | 客户端当前版本号 |
| `software` | string | 否 | 软件标识，不传则检查默认软件 |

**透传目标**：`GET <backend>/public/downloads/check?platform=...&version=...&software=...`

**版本号格式**：`vMAJOR.MINOR.PATCH`（如 `v1.2.0`），支持带或不带 `v` 前缀。

**响应字段**：

| 字段 | 类型 | 说明 |
|------|------|------|
| `software_name` | string | 软件名称 |
| `platform` | string | 平台标识 |
| `current_version` | string | 客户端当前版本 |
| `latest_version` | string | 最新版本号 |
| `needs_update` | bool | 是否需要更新 |
| `force_update` | bool | 是否强制更新（`needs_update=true` 且该版本标记为强制更新时为 `true`） |
| `download_url` | string | 下载地址（需要更新时返回） |
| `file_type` | string | 文件类型（如 `dmg`、`exe`、`zip`） |
| `changelog` | string | 更新日志（可选，有内容时返回） |

**响应示例**（有更新，强制更新）：

```json
{
  "software_name": "aliang-helper",
  "platform": "windows",
  "current_version": "v1.2.0",
  "latest_version": "v2.0.0",
  "needs_update": true,
  "force_update": true,
  "download_url": "https://example.com/download/aliang-helper-2.0.0.exe",
  "file_type": "exe",
  "changelog": "修复安全漏洞，必须升级"
}
```

**响应示例**（有更新，非强制）：

```json
{
  "software_name": "aliang-helper",
  "platform": "windows",
  "current_version": "v1.2.0",
  "latest_version": "v1.3.0",
  "needs_update": true,
  "force_update": false,
  "download_url": "https://example.com/download/aliang-helper-1.3.0.exe",
  "file_type": "exe",
  "changelog": "新增主题切换功能"
}
```

**响应示例**（已是最新）：

```json
{
  "software_name": "aliang-helper",
  "platform": "windows",
  "current_version": "v1.3.0",
  "latest_version": "v1.3.0",
  "needs_update": false,
  "force_update": false,
  "download_url": "https://example.com/download/aliang-helper-1.3.0.exe",
  "file_type": "exe"
}
```

**错误响应**：

| 状态码 | 说明 |
|--------|------|
| 400 | `platform` 和 `version` 为必填；版本号格式无效 |
| 404 | 未找到匹配的下载项 |

**强制更新说明**：

后端数据库 `als_downloads` 表中每条下载记录有 `force_update` 字段（布尔值，默认 `false`）。管理员在发布新版本时可将该版本标记为强制更新。`force_update` 仅在 `needs_update=true`（确实需要更新）时才会返回 `true`，如果用户已是最新版本，即使最新版本标记了强制更新，也不会返回 `true`。

### 2.2 管理接口 — 下载中心

以下接口需要 `Authorization` 请求头。

#### 列表与创建

```
GET  /api/admin/download-center
POST /api/admin/download-center
```

**GET** — 获取所有下载项列表

**透传目标**：`GET <backend>/admin/download-center`

**POST** — 创建新的下载项

**透传目标**：`POST <backend>/admin/download-center`

**请求体**（POST）：

```json
{
  "name": "Aliang Helper",
  "platform": "windows",
  "version": "1.2.0",
  "download_url": "https://example.com/download/aliang-helper-1.2.0.exe",
  "file_size": 52428800,
  "release_notes": "Bug fixes"
}
```

#### 单项操作

```
GET    /api/admin/download-center/{id}
PUT    /api/admin/download-center/{id}
DELETE /api/admin/download-center/{id}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `id` | string | 下载项 ID |

**透传目标**：`<backend>/admin/download-center/{id}`

---

## 三、配置中心

### 3.1 软件配置管理

以下接口需要 `Authorization` 请求头。

#### 软件列表与创建

```
GET  /api/admin/config-center/software
POST /api/admin/config-center/software
```

**透传目标**：`<backend>/admin/config-center/software`

**GET** — 获取所有软件配置

**POST** — 创建新的软件配置

#### 单个软件操作

```
GET    /api/admin/config-center/software/{code}
PUT    /api/admin/config-center/software/{code}
DELETE /api/admin/config-center/software/{code}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `code` | string | 软件编码标识 |

**透传目标**：`<backend>/admin/config-center/software/{code}`

#### 软件标签管理

```
POST   /api/admin/config-center/software/{code}/tags
DELETE /api/admin/config-center/software/{code}/tags/{tag}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `code` | string | 软件编码标识 |
| `tag` | string | 标签名 |

**透传目标**：
- `POST → <backend>/admin/config-center/software/{code}/tags`
- `DELETE → <backend>/admin/config-center/software/{code}/tags/{tag}`

**POST 请求体**：标签数据（JSON）

#### 软件模板管理

```
GET  /api/admin/config-center/software/{code}/templates
POST /api/admin/config-center/software/{code}/templates
```

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `code` | string | 软件编码标识 |

**透传目标**：`<backend>/admin/config-center/software/{code}/templates`

**GET** — 获取该软件的所有模板

**POST** — 创建新模板

### 3.2 模板操作

```
PUT    /api/admin/config-center/templates/{id}
DELETE /api/admin/config-center/templates/{id}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `id` | string | 模板 ID |

**透传目标**：`<backend>/admin/config-center/templates/{id}`

### 3.3 全局变量管理

```
GET    /api/admin/config-center/global-vars
POST   /api/admin/config-center/global-vars
DELETE /api/admin/config-center/global-vars/{key}
```

**透传目标**：`<backend>/admin/config-center/global-vars[/{key}]`

**GET** — 获取所有全局变量

**POST** — 创建或更新全局变量

**DELETE** — 删除指定全局变量

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `key` | string | 全局变量的键名 |

### 3.4 配置同步

以下接口需要 `Authorization` 请求头。

#### 触发同步

```
POST /api/configs/sync
```

**透传目标**：`POST <backend>/api/v1/configs/sync`

**请求体**：同步参数（JSON）

#### 同步状态

```
GET /api/configs/sync-status
```

**透传目标**：`GET <backend>/api/v1/configs/sync/status`

**说明**：查询当前配置同步任务的状态

#### 删除同步任务

```
DELETE /api/configs/sync/{uuid}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `uuid` | string | 同步任务的 UUID |

**透传目标**：`DELETE <backend>/api/v1/configs/sync/{uuid}`

#### 配置对比

```
POST /api/configs/compare
```

**透传目标**：`POST <backend>/api/v1/configs/compare`

**请求体**：需要对比的配置数据（JSON）

### 3.5 公共配置查询

#### 软件列表

```
GET /api/configs/software-list
```

**透传目标**：`GET <backend>/api/v1/configs/software-list`

**认证**：需要 `Authorization` 请求头

#### 默认配置

```
GET /api/configs/default
```

**认证**：需要 `Authorization` 请求头

**Query 参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `tag` | string | 否 | 按标签筛选配置 |

**透传目标**：`GET <backend>/api/v1/configs/default?tag=...`

---

## 四、接口总览

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| GET | `/api/health` | 无 | 服务健康评分（2s 缓存） |
| GET | `/api/runtime-config` | 无 | 运行时后端地址 |
| GET | `/api/public/downloads` | 无 | 下载列表 |
| GET | `/api/public/downloads/check` | 无 | 版本更新检查 |
| GET | `/api/admin/download-center` | 需要 | 下载中心列表 |
| POST | `/api/admin/download-center` | 需要 | 创建下载项 |
| GET | `/api/admin/download-center/{id}` | 需要 | 获取下载项 |
| PUT | `/api/admin/download-center/{id}` | 需要 | 更新下载项 |
| DELETE | `/api/admin/download-center/{id}` | 需要 | 删除下载项 |
| GET | `/api/admin/config-center/software` | 需要 | 软件配置列表 |
| POST | `/api/admin/config-center/software` | 需要 | 创建软件配置 |
| GET | `/api/admin/config-center/software/{code}` | 需要 | 获取软件配置 |
| PUT | `/api/admin/config-center/software/{code}` | 需要 | 更新软件配置 |
| DELETE | `/api/admin/config-center/software/{code}` | 需要 | 删除软件配置 |
| POST | `/api/admin/config-center/software/{code}/tags` | 需要 | 添加标签 |
| DELETE | `/api/admin/config-center/software/{code}/tags/{tag}` | 需要 | 删除标签 |
| GET | `/api/admin/config-center/software/{code}/templates` | 需要 | 软件模板列表 |
| POST | `/api/admin/config-center/software/{code}/templates` | 需要 | 创建软件模板 |
| PUT | `/api/admin/config-center/templates/{id}` | 需要 | 更新模板 |
| DELETE | `/api/admin/config-center/templates/{id}` | 需要 | 删除模板 |
| GET | `/api/admin/config-center/global-vars` | 需要 | 全局变量列表 |
| POST | `/api/admin/config-center/global-vars` | 需要 | 创建/更新全局变量 |
| DELETE | `/api/admin/config-center/global-vars/{key}` | 需要 | 删除全局变量 |
| POST | `/api/configs/sync` | 需要 | 触发配置同步 |
| GET | `/api/configs/sync-status` | 需要 | 同步状态 |
| DELETE | `/api/configs/sync/{uuid}` | 需要 | 删除同步任务 |
| POST | `/api/configs/compare` | 需要 | 配置对比 |
| GET | `/api/configs/software-list` | 需要 | 公共软件列表 |
| GET | `/api/configs/default` | 需要 | 默认配置 |
