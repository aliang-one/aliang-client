# 快速开始: 仪表板重构与流量监控优化

**日期**: 2025-12-17
**功能**: 仪表板重构与流量监控优化
**版本**: v1

---

## 概述

本文档指导开发者开始实施本功能的各个部分。功能分为三个优先级:
- **P1**: 统一操作面板(页面合并 + 布局优化)
- **P2**: 统一规则引擎配置(Nacos + 前端配置UI)
- **P3**: 后端流量统计(多时间尺度缓存)

---

## 环境设置

### 前置条件

- Go 1.25.1
- Node.js 16+ (可选，仅用于前端构建工具)
- Nacos服务可用
- Docker (可选，用于Nacos本地测试)

### 本地开发环境

1. **克隆仓库**:
```bash
cd /Users/mac/MyProgram/GoProgram/nursor/nursorgate2
git checkout 002-refactor-dashboard-traffic
```

2. **验证后端依赖**:
```bash
go mod tidy
go mod download
```

3. **启动Nacos(如果使用本地版本)**:
```bash
docker run -d --name nacos-server \
  -e MODE=standalone \
  -p 8848:8848 \
  nacos/nacos-server:latest
```

4. **运行后端服务**:
```bash
go run cmd/nursorgate/main.go
# 访问前端: http://localhost:56431
```

---

## 实施步骤

### 第1阶段: P1 - 统一操作面板(前端)

**预期时间**: 2-3天
**主要文件**: `app/website/index.html`, `app/website/assets/app.js`, `app/website/assets/styles.css`

#### 1.1 合并"代理管理"和"运行控制"页面

**目标**: 创建单一的"代理管理"页面，包含所有代理和运行控制功能

**步骤**:

1. 打开 `app/website/index.html`，定位到现有的"代理管理"和"运行控制"页面定义

2. 创建合并的页面结构(使用Bootstrap标签页):

```html
<!-- 新的合并页面 -->
<div id="proxy-control-page" class="page">
  <div class="container-fluid">
    <h2>代理管理与运行控制</h2>

    <!-- 标签页 -->
    <ul class="nav nav-tabs" role="tablist">
      <li class="nav-item">
        <a class="nav-link active" data-bs-toggle="tab" href="#proxy-management">
          代理管理
        </a>
      </li>
      <li class="nav-item">
        <a class="nav-link" data-bs-toggle="tab" href="#run-control">
          运行控制
        </a>
      </li>
    </ul>

    <!-- 标签页内容 -->
    <div class="tab-content">
      <!-- 代理管理标签页 -->
      <div id="proxy-management" class="tab-pane fade show active">
        <!-- 现有的代理管理功能 -->
      </div>

      <!-- 运行控制标签页 -->
      <div id="run-control" class="tab-pane fade">
        <!-- 现有的运行控制功能 -->
      </div>
    </div>
  </div>
</div>
```

3. 更新 `app.js` 中的页面导航逻辑，移除对两个页面的单独处理，改为使用合并后的页面

#### 1.2 优化仪表板布局

**目标**: 将Dashboard布局优化为:
- 关键指标区域: 占≤30%
- 流量监控区域: 占≥50%
- 其他内容: ≤20%

**步骤**:

1. 修改 `index.html` 中的Dashboard布局:

```html
<div id="dashboard-page" class="page">
  <div class="container-fluid h-100 d-flex flex-column">
    <!-- 关键指标区域 (≤30%) -->
    <div class="metrics-section" style="flex: 0 0 30%; overflow-y: auto;">
      <div class="row g-2">
        <!-- 8个指标卡片 -->
        <div class="col-lg-3 col-md-6">
          <div class="card">
            <div class="card-body">
              <h6 class="card-title">运行状态</h6>
              <p class="card-text" id="status-value">加载中...</p>
            </div>
          </div>
        </div>
        <!-- ... 其他7个指标 ... -->
      </div>
    </div>

    <!-- 流量监控区域 (≥50%) -->
    <div class="traffic-section" style="flex: 1 1 50%; overflow-y: auto; min-height: 400px;">
      <div class="card">
        <div class="card-header">
          <h5 class="card-title">实时流量监控</h5>
          <div class="btn-group" role="group">
            <button type="button" class="btn btn-sm btn-outline-primary" data-timescale="1s">1秒</button>
            <button type="button" class="btn btn-sm btn-outline-primary" data-timescale="5s">5秒</button>
            <button type="button" class="btn btn-sm btn-outline-primary" data-timescale="15s">15秒</button>
          </div>
        </div>
        <div class="card-body">
          <canvas id="traffic-chart"></canvas>
        </div>
      </div>
    </div>

    <!-- 其他内容 (≤20%) -->
    <div class="other-section" style="flex: 0 0 20%; overflow-y: auto;">
      <!-- 补充信息 -->
    </div>
  </div>
</div>
```

2. 更新 `styles.css` 支持新的布局:

```css
.dashboard {
  height: 100%;
  display: flex;
  flex-direction: column;
}

.metrics-section {
  border-bottom: 1px solid #ddd;
  padding: 15px;
}

.traffic-section {
  flex: 1;
  padding: 15px;
  overflow-y: auto;
}

.other-section {
  border-top: 1px solid #ddd;
  padding: 15px;
}

/* 指标卡片紧凑样式 */
.metrics-section .card {
  padding: 10px;
  border: 1px solid #e9ecef;
}

.metrics-section .card-body {
  padding: 8px;
}

.metrics-section .card-title {
  font-size: 0.85rem;
  margin-bottom: 5px;
}

.metrics-section .card-text {
  font-size: 0.9rem;
  font-weight: bold;
}
```

3. 在 `app.js` 中添加页面导航事件，确保点击导航菜单时显示合并后的页面

**验证检查清单**:
- [ ] 合并页面可以正常显示
- [ ] 标签页切换功能正常
- [ ] 仪表板布局符合30%-50%-20%的比例
- [ ] 页面在不同屏幕尺寸下响应式正常

---

### 第2阶段: P2 - 统一规则引擎配置(前后端)

**预期时间**: 3-4天
**主要文件**: `common/model/routing_config.go`, `app/website/index.html`, `app/website/assets/app.js`

#### 2.1 创建统一的配置模型

**目标**: 在 `common/model` 中定义Go结构体

**步骤**:

1. 创建文件 `common/model/routing_config.go`:

```bash
touch /Users/mac/MyProgram/GoProgram/nursor/nursorgate2/common/model/routing_config.go
```

2. 根据 `data-model.md` 中的设计填写结构体定义

3. 添加验证方法:

```go
func (rc *RoutingRulesConfig) Validate() error {
    // 验证每条规则
    // 检查ID唯一性
    // 验证condition格式
    return nil
}
```

4. 添加JSON序列化/反序列化支持:

```go
func (rc *RoutingRulesConfig) ToJSON() ([]byte, error) {
    return json.Marshal(rc)
}

func NewRoutingRulesConfigFromJSON(data []byte) (*RoutingRulesConfig, error) {
    var rc RoutingRulesConfig
    if err := json.Unmarshal(data, &rc); err != nil {
        return nil, err
    }
    if err := rc.Validate(); err != nil {
        return nil, err
    }
    return &rc, nil
}
```

#### 2.2 实现后端API端点

**目标**: 创建 `/api/config/routing` 端点

**步骤**:

1. 创建API处理器文件 `cmd/nursorgate/handlers/config.go`

2. 实现GET和POST端点，参考 `contracts/routing-config-api.md`

3. 添加Nacos集成逻辑:

```go
func (h *ConfigHandler) GetRoutingConfig() {
    // 从Nacos读取配置
    configContent, err := h.nacosClient.GetConfig("routing-rules")
    if err != nil {
        // 返回错误
    }
    // 解析为RoutingRulesConfig
    config, err := model.NewRoutingRulesConfigFromJSON([]byte(configContent))
    // 返回JSON
}

func (h *ConfigHandler) UpdateRoutingConfig(req *model.RoutingRulesConfig) {
    // 验证
    if err := req.Validate(); err != nil {
        // 返回验证错误
    }
    // 写入Nacos
    data, _ := req.ToJSON()
    err := h.nacosClient.PublishConfig("routing-rules", string(data))
    // 返回成功/错误
}
```

#### 2.3 实现前端配置UI

**目标**: 在前端添加规则引擎配置页面

**步骤**:

1. 在 `index.html` 中添加规则引擎页面:

```html
<div id="rules-engine-page" class="page">
  <div class="container-fluid">
    <h2>规则引擎配置</h2>

    <!-- 全局设置 -->
    <div class="card mb-3">
      <div class="card-header">全局设置</div>
      <div class="card-body">
        <div class="form-check">
          <input class="form-check-input" type="checkbox" id="geoipToggle" checked>
          <label class="form-check-label" for="geoipToggle">
            启用GeoIP判断
          </label>
        </div>
        <div class="form-check">
          <input class="form-check-input" type="checkbox" id="nonelaneToggle">
          <label class="form-check-label" for="nonelaneToggle">
            启用NoneLane代理
          </label>
        </div>
      </div>
    </div>

    <!-- 三个规则集编辑区 -->
    <ul class="nav nav-tabs" role="tablist">
      <li class="nav-item">
        <a class="nav-link active" data-bs-toggle="tab" href="#to-door-rules">
          To Door规则
        </a>
      </li>
      <li class="nav-item">
        <a class="nav-link" data-bs-toggle="tab" href="#blacklist-rules">
          黑名单
        </a>
      </li>
      <li class="nav-item">
        <a class="nav-link" data-bs-toggle="tab" href="#nonelane-rules">
          NoneLane规则
        </a>
      </li>
    </ul>

    <div class="tab-content">
      <div id="to-door-rules" class="tab-pane fade show active">
        <!-- 规则列表和编辑界面 -->
      </div>
      <div id="blacklist-rules" class="tab-pane fade">
        <!-- 规则列表和编辑界面 -->
      </div>
      <div id="nonelane-rules" class="tab-pane fade">
        <!-- 规则列表和编辑界面 -->
      </div>
    </div>

    <!-- 保存按钮 -->
    <button type="button" class="btn btn-primary" id="saveConfigBtn">
      保存配置
    </button>
  </div>
</div>
```

2. 在 `app.js` 中添加规则管理逻辑:

```javascript
async function loadRoutingConfig() {
    const response = await fetch('/api/config/routing', {
        headers: { 'Authorization': `Bearer ${token}` }
    });
    const result = await response.json();
    if (result.success) {
        populateRulesUI(result.data);
    }
}

async function saveRoutingConfig() {
    const config = extractConfigFromUI();
    const response = await fetch('/api/config/routing', {
        method: 'POST',
        headers: {
            'Authorization': `Bearer ${token}`,
            'Content-Type': 'application/json'
        },
        body: JSON.stringify(config)
    });
    // 处理响应
}
```

**验证检查清单**:
- [ ] 配置模型定义完整
- [ ] GET /api/config/routing 能正确返回配置
- [ ] POST /api/config/routing 能正确保存配置到Nacos
- [ ] 前端能加载和显示规则列表
- [ ] 前端能编辑和保存规则
- [ ] 启用/禁用开关功能正常

---

### 第3阶段: P3 - 后端流量统计(后端)

**预期时间**: 2-3天
**主要文件**: `processor/stats/collector.go`, `processor/stats/cache.go`, API端点

#### 3.1 实现统计收集器

**目标**: 创建在后台定期收集流量统计数据

**步骤**:

1. 创建 `processor/stats/collector.go`:

```bash
mkdir -p /Users/mac/MyProgram/GoProgram/nursor/nursorgate2/processor/stats
touch /Users/mac/MyProgram/GoProgram/nursor/nursorgate2/processor/stats/collector.go
```

2. 实现StatsCollector结构体和Collect方法(参考research.md中的设计)

3. 在后台启动定时任务:

```go
func (c *StatsCollector) Start() {
    go c.collectEvery1Second()
    go c.collectEvery5Seconds()
    go c.collectEvery15Seconds()
}

func (c *StatsCollector) collectEvery1Second() {
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        stats := c.gatherStats()
        c.cache1s.Push(stats)
    }
}
```

#### 3.2 实现缓存存储

**目标**: 创建环形缓冲区存储最近300条记录

**步骤**:

1. 创建 `processor/stats/cache.go`:

```bash
touch /Users/mac/MyProgram/GoProgram/nursor/nursorgate2/processor/stats/cache.go
```

2. 实现RingBuffer:

```go
type RingBuffer struct {
    data []*TrafficStats
    capacity int
    head int
    tail int
    count int
    mu sync.RWMutex
}

func (rb *RingBuffer) Push(item *TrafficStats) {
    rb.mu.Lock()
    defer rb.mu.Unlock()
    // 实现FIFO逻辑
}

func (rb *RingBuffer) GetAll() []*TrafficStats {
    rb.mu.RLock()
    defer rb.mu.RUnlock()
    // 返回所有项
}
```

#### 3.3 实现API端点

**目标**: 创建 `/api/stats/{timescale}` 端点

**步骤**:

1. 创建API处理器 `cmd/nursorgate/handlers/stats.go`

2. 实现GET端点，参考 `contracts/traffic-stats-api.md`

3. 在路由中注册端点

**验证检查清单**:
- [ ] 统计收集器正确收集数据
- [ ] 后台任务正常运行
- [ ] 缓存存储正确维护最近300条
- [ ] GET /api/stats/1s 返回正确数据
- [ ] 返回数据包含active_connections和历史stats

#### 3.4 更新前端显示实时流量

**目标**: 在Dashboard中实现实时流量监控图表

**步骤**:

1. 在Dashboard中添加Chart.js库(如未添加)

2. 实现前端刷新逻辑(参考contracts/traffic-stats-api.md中的示例代码)

3. 实现时间尺度切换

**验证检查清单**:
- [ ] 图表正确显示流量数据
- [ ] 每秒自动刷新一次
- [ ] 时间尺度切换正常
- [ ] 数据延迟不超过2秒
- [ ] 图表不会频繁闪烁

---

## 测试检查清单

### 单元测试

- [ ] RoutingRulesConfig.Validate() 测试所有边界情况
- [ ] RuleType匹配逻辑测试(domain/ip/geoip)
- [ ] RingBuffer FIFO逻辑测试
- [ ] TrafficStats数据收集测试

### 集成测试

- [ ] GET /api/config/routing 端到端测试
- [ ] POST /api/config/routing 端到端测试
- [ ] Nacos配置读写测试
- [ ] GET /api/stats/{timescale} 端到端测试
- [ ] 前端UI与后端API集成测试

### 手动测试

- [ ] 访问Dashboard，验证布局符合要求
- [ ] 代理切换，验证仪表板实时更新
- [ ] 进入规则引擎页面，加载现有配置
- [ ] 新增/编辑/删除规则，保存到Nacos
- [ ] 启用/禁用GeoIP和NoneLane选项
- [ ] 观察实时流量图表，验证数据准确性
- [ ] 切换时间尺度(1s/5s/15s)，验证数据切换
- [ ] 性能测试: 代理判断延迟<200ms，API响应<50ms

---

## 常见问题与排查

### 问题: Nacos连接失败

**症状**: GET /api/config/routing 返回500错误

**排查**:
1. 检查Nacos服务状态: `docker ps | grep nacos`
2. 检查Nacos连接配置: `config/nacos.conf`
3. 查看后端日志: `grep -i nacos *.log`

### 问题: 流量统计数据为零

**症状**: 流量图表显示全零

**排查**:
1. 确认后端StatsCollector已启动
2. 检查数据收集逻辑: `processor/stats/collector.go`
3. 验证缓存是否正确存储: 添加临时日志输出

### 问题: 前端页面合并后功能丢失

**症状**: 切换标签页时某些功能不工作

**排查**:
1. 检查页面ID和标签页ID是否一致
2. 查看浏览器控制台JavaScript错误
3. 验证app.js中的页面导航逻辑

---

## 参考资源

- **规范**: `spec.md`
- **数据模型**: `data-model.md`
- **API契约**: `contracts/routing-config-api.md`, `contracts/traffic-stats-api.md`
- **技术决策**: `research.md`

---

## 进度追踪

使用以下命令跟踪实施进度:

```bash
# 检查当前分支
git branch -v

# 查看规范文件
cat specs/002-refactor-dashboard-traffic/spec.md

# 查看任务列表(生成后)
cat specs/002-refactor-dashboard-traffic/tasks.md
```
