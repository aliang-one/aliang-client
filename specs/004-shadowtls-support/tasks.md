# 任务: ShadowTLS 协议支持

**输入**: 来自 `/specs/004-shadowtls-support/` 的设计文档
**前置条件**: plan.md(✅)、spec.md(✅)、research.md(✅)、data-model.md(✅)、contracts/(✅)、quickstart.md(✅)

**规范**: [spec.md](./spec.md) | **计划**: [plan.md](./plan.md) | **数据模型**: [data-model.md](./data-model.md)

**项目结构**: Go 单一模块，遵循现有代理实现模式(VLESS、Shadowsocks)

**测试策略**: 单元测试 + 集成测试(spec.md 明确要求)

---

## 阶段 1: 设置(共享基础设施)

**目的**: 项目初始化和 ShadowTLS 代理基本结构

- [ ] T001 根据实施计划创建 `outbound/proxy/shadowtls/` 目录结构
- [ ] T002 [P] 在 `processor/config/types.go` 中扩展 `ShadowsocksConfig` 结构，添加 `Plugin` 和 `PluginOpts` 字段
- [ ] T003 [P] 在 `processor/config/types.go` 中实现 `ShadowTLSPluginOpts` 结构体定义
- [ ] T004 [P] 在 `processor/config/types.go` 中为 `ShadowTLSPluginOpts` 实现 `Validate()` 方法

**检查点**: ShadowTLS 代理目录结构创建完成，配置结构扩展完成

---

## 阶段 2: 基础(阻塞前置条件)

**目的**: 在任何用户故事可以实施之前必须完成的核心基础设施

**⚠️ 关键**: 在此阶段完成之前，无法开始任何用户故事工作

- [ ] T005 [P] 在 `outbound/proxy/shadowtls/shadowtls.go` 中创建 `ShadowTLS` 代理基础结构体，实现 `proxy.Proxy` 接口
- [ ] T006 [P] 在 `outbound/proxy/shadowtls/shadowtls.go` 中为 `ShadowTLS` 实现 `Addr()` 方法
- [ ] T007 [P] 在 `outbound/proxy/shadowtls/shadowtls.go` 中为 `ShadowTLS` 实现 `Proto()` 方法，返回 `proto.ShadowTLS`
- [ ] T008 为 `ShadowTLS` 代理在 `outbound/proxy/interfaces.go` 或相关文件中添加 `proto.ShadowTLS` 常量定义
- [ ] T009 [P] 在 `outbound/proxy/shadowtls/shadowtls.go` 中创建 `New()` 工厂函数用于创建 ShadowTLS 代理实例
- [ ] T010 在 `processor/config/types.go` 中的 `ShadowsocksConfig` 上实现 `Validate()` 方法，包含 plugin 验证逻辑
- [ ] T011 在 `cmd/start.go` 或适当的启动文件中实现协议选择逻辑：当 `plugin: "shadow-tls"` 时创建 ShadowTLS 代理，否则创建标准 Shadowsocks
- [ ] T012 在启动时配置解析流程中添加 ShadowTLS 配置加载逻辑，确保在命令执行时正确识别和初始化 ShadowTLS 代理

**检查点**: 基础就绪 - ShadowTLS 代理框架完成，协议选择逻辑完成，现在可以开始并行实施用户故事

---

## 阶段 3: 用户故事 1 - 配置 Shadowsocks + ShadowTLS 插件代理 (优先级: P1) 🎯 MVP

**目标**: 系统能够正确解析和加载包含 ShadowTLS 插件的 Shadowsocks 配置，创建相应的代理实例

**独立测试**: 可以通过加载包含 `plugin: shadow-tls` 的配置文件，验证系统是否正确识别 ShadowTLS 插件并创建 ShadowTLS 代理实例而非标准 Shadowsocks 实例

### 用户故事 1 的测试

**注意: 先编写这些测试，确保在实施前它们失败**

- [ ] T013 [P] [US1] 在 `processor/config/types_test.go` 中为 `ShadowTLSPluginOpts.Validate()` 编写单元测试，覆盖：有效参数、缺少 host、缺少 password、password 长度 < 8、无效 version
- [ ] T014 [P] [US1] 在 `processor/config/types_test.go` 中为 `ShadowsocksConfig.Validate()` 编写单元测试，覆盖：plugin="shadow-tls" 时 plugin_opts 必需、plugin 与 plugin_opts 的一致性验证
- [ ] T015 [P] [US1] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写配置解析和创建代理实例的单元测试

### 用户故事 1 的实施

- [ ] T016 [US1] 在 `processor/config/types_test.go` 中运行测试确保 `ShadowTLSPluginOpts.Validate()` 所有验证规则正确实施
- [ ] T017 [US1] 在 `processor/config/types_test.go` 中运行测试确保 `ShadowsocksConfig.Validate()` 插件验证逻辑正确
- [ ] T018 [P] [US1] 在 `outbound/proxy/shadowtls/shadowtls.go` 中完善 `New()` 函数，确保正确接收和存储 ShadowTLSPluginOpts
- [ ] T019 [P] [US1] 在 `cmd/start.go` 中确保配置加载时正确调用协议选择逻辑，验证配置有效性
- [ ] T020 [US1] 编写集成测试验证：加载带 `plugin: shadow-tls` 的配置 → 系统创建 ShadowTLS 代理而非标准 Shadowsocks
- [ ] T021 [US1] 编写集成测试验证：加载不带 plugin 的 Shadowsocks 配置 → 系统创建标准 Shadowsocks 代理
- [ ] T022 [US1] 编写集成测试验证：加载无效 ShadowTLS 配置 → 返回清晰的错误消息指出缺失字段

**检查点**: 用户故事 1 应该完全功能化且可独立测试 - 系统能够正确识别、解析和创建 ShadowTLS 代理实例

---

## 阶段 4: 用户故事 2 - 通过 ShadowTLS 代理转发 TCP 连接 (优先级: P1)

**目标**: 系统能够通过已配置的 ShadowTLS 代理建立和维护 TCP 连接，转发数据

**独立测试**: 可以配置一个 ShadowTLS 代理，发起 TCP 连接请求，验证连接是否成功建立和数据是否能正确通过代理转发

### 用户故事 2 的测试

**注意: 先编写这些测试，确保在实施前它们失败**

- [ ] T023 [P] [US2] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写 `DialContext()` 方法的单元测试，模拟 TLS 握手和 Shadowsocks 连接
- [ ] T024 [P] [US2] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写 `DialUDP()` 方法的单元测试，验证是否返回 unsupported error

### 用户故事 2 的实施

- [ ] T025 [P] [US2] 在 `outbound/proxy/shadowtls/protocol.go` 中实现 TLS 握手逻辑：建立到 ShadowTLS 服务器的 TCP 连接，与伪装域名进行 TLS 握手
- [ ] T026 [P] [US2] 在 `outbound/proxy/shadowtls/protocol.go` 中实现 ShadowTLS 认证逻辑：在 TLS ApplicationData 阶段发送 HMAC 认证
- [ ] T027 [P] [US2] 在 `outbound/proxy/shadowtls/connection.go` 中实现连接管理和数据转发逻辑
- [ ] T028 [US2] 在 `outbound/proxy/shadowtls/shadowtls.go` 中实现 `DialContext()` 方法，协调 TLS 握手、ShadowTLS 认证、Shadowsocks 连接建立和返回连接
- [ ] T029 [US2] 在 `outbound/proxy/shadowtls/shadowtls.go` 中实现 `DialUDP()` 方法，返回 unsupported error
- [ ] T030 [US2] 编写集成测试：通过 ShadowTLS 代理建立 TCP 连接到实际目标服务器，验证连接成功建立
- [ ] T031 [US2] 编写集成测试：验证连接建立时间 < 3 秒（性能标准 SC-002）
- [ ] T032 [US2] 编写集成测试：测试目标不可达情况，验证系统返回适当的错误消息

**检查点**: 用户故事 2 完成后，系统应该能够通过 ShadowTLS 代理进行实际的 TCP 数据转发

---

## 阶段 5: 用户故事 3 - 支持多种加密方式和协议组合 (优先级: P2)

**目标**: ShadowTLS 代理支持 Shadowsocks 的所有加密方式，确保灵活的加密方式选择

**独立测试**: 可以配置使用不同加密方式(chacha20-poly1305、aes-256-gcm 等)的 ShadowTLS 代理，验证每种配置能否正确初始化并工作

### 用户故事 3 的测试

- [ ] T033 [P] [US3] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写单元测试，验证所有支持的加密方式(aes-128-gcm、aes-256-gcm、chacha20-poly1305 等)都能正确初始化
- [ ] T034 [P] [US3] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写单元测试，验证不支持的加密方式返回错误

### 用户故事 3 的实施

- [ ] T035 [P] [US3] 在 `outbound/proxy/shadowtls/shadowtls.go` 中添加加密方式验证逻辑，确保只接受 Shadowsocks 支持的方式
- [ ] T036 [US3] 在 `processor/config/types.go` 的 `ShadowsocksConfig.Validate()` 中添加加密方式的有效性检查
- [ ] T037 [US3] 编写集成测试：配置使用 chacha20-ietf-poly1305 的 ShadowTLS 代理，验证连接成功
- [ ] T038 [US3] 编写集成测试：配置使用 aes-256-gcm 的 ShadowTLS 代理，验证连接成功
- [ ] T039 [US3] 编写集成测试：尝试配置不支持的加密方式，验证返回清晰的错误消息

**检查点**: 用户故事 3 完成后，系统应该支持所有主流加密方式

---

## 阶段 6: 用户故事 4 - 错误处理和连接失败 (优先级: P2)

**目标**: 系统在连接失败时提供有意义的错误消息，帮助用户快速定位问题

**独立测试**: 配置错误的 ShadowTLS 参数或模拟连接失败场景，验证系统返回清晰的错误消息

### 用户故事 4 的测试

- [ ] T040 [P] [US4] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写单元测试：TLS 握手失败场景(无效域名、证书验证失败)
- [ ] T041 [P] [US4] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写单元测试：ShadowTLS 认证失败场景(密码错误)
- [ ] T042 [P] [US4] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写单元测试：网络错误场景(连接超时、无法访问服务器)
- [ ] T043 [P] [US4] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中编写单元测试：配置错误场景(缺少必需参数)

### 用户故事 4 的实施

- [ ] T044 [P] [US4] 在 `outbound/proxy/shadowtls/protocol.go` 中为 TLS 握手失败实现清晰的错误消息(包含目标域名、失败原因)
- [ ] T045 [P] [US4] 在 `outbound/proxy/shadowtls/protocol.go` 中为 ShadowTLS 认证失败实现清晰的错误消息(提示检查密码)
- [ ] T046 [P] [US4] 在 `outbound/proxy/shadowtls/shadowtls.go` 中为 `DialContext()` 中的各个失败点添加错误处理和日志记录
- [ ] T047 [US4] 在 `processor/config/types.go` 中为配置验证错误实现详细的错误消息
- [ ] T048 [US4] 编写集成测试：伪装域名不可访问 → 返回 "TLS handshake failed to [host]" 错误
- [ ] T049 [US4] 编写集成测试：ShadowTLS 密码错误 → 返回 "authentication failed (check password)" 错误
- [ ] T050 [US4] 编写集成测试：服务器端口错误 → 返回连接超时错误
- [ ] T051 [US4] 编写集成测试：缺少 plugin_opts.host → 返回 "plugin_opts.host is required" 错误

**检查点**: 用户故事 4 完成后，所有错误情况都应该返回清晰、有用的错误消息

---

## 阶段 7: 完善与横切关注点

**目的**: 性能优化、文档完善、代码质量和整体验证

- [ ] T052 [P] 在 `outbound/proxy/shadowtls/shadowtls.go` 中实现 TLS 会话缓存优化，减少 TLS 握手开销
- [ ] T053 [P] 在 `outbound/proxy/shadowtls/connection.go` 中实现连接池和复用逻辑(如协议允许)
- [ ] T054 [P] 在 `outbound/proxy/shadowtls/shadowtls.go` 中添加读写缓冲区优化
- [ ] T055 [P] 运行 `go fmt ./outbound/proxy/shadowtls/` 和 `go vet ./outbound/proxy/shadowtls/` 进行代码格式化和检查
- [ ] T056 [P] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中添加性能基准测试(BenchmarkDialContext)，验证连接建立时间 < 3 秒
- [ ] T057 [P] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中添加并发测试(TestConcurrentConnections)，验证支持 100+ 并发连接
- [ ] T058 [P] 在 `outbound/proxy/shadowtls/shadowtls_test.go` 中添加内存泄漏测试，确保连接正确关闭
- [ ] T059 运行完整的单元测试套件：`go test ./outbound/proxy/shadowtls/...`，确保所有测试通过
- [ ] T060 运行完整的集成测试，验证 US1-US4 所有场景
- [ ] T061 [P] 在 `docs/` 或适当位置更新文档，包含 ShadowTLS 配置说明
- [ ] T062 [P] 验证 `specs/004-shadowtls-support/quickstart.md` 中的配置示例能否真实运行
- [ ] T063 运行 `go mod tidy` 和 `go mod verify` 确保依赖一致性
- [ ] T064 代码审查：确保代码遵循项目现有风格(参考 VLESS、Shadowsocks 实现)
- [ ] T065 资源清理：确保所有连接、TLS 会话都被正确关闭，无资源泄漏
- [ ] T066 创建 CHANGELOG 条目，记录 ShadowTLS 支持功能
- [ ] T067 验证初始化延迟 < 1 秒(性能标准 SC-001)

**检查点**: 所有优化、测试和文档完成后，ShadowTLS 协议支持功能已准备好进入生产

---

## 依赖关系与执行顺序

### 阶段依赖关系

```
设置(阶段 1) ──→ 基础(阶段 2) ──→ ┌─ US1(阶段 3)
                                │
                                ├─ US2(阶段 4) [可与 US1 并行]
                                │
                                ├─ US3(阶段 5) [可与 US1/US2 并行]
                                │
                                └─ US4(阶段 6) [可与 US1/US2/US3 并行]
                                       ↓
                                完善(阶段 7)
```

### 用户故事依赖关系

- **用户故事 1(P1)**: 可在基础(阶段 2)后开始 → 无其他故事依赖 → MVP 交付点
- **用户故事 2(P1)**: 可在基础(阶段 2)后开始 → 推荐在 US1 后，但不强制依赖
- **用户故事 3(P2)**: 可在基础(阶段 2)后开始 → 可与 US1/US2 并行
- **用户故事 4(P2)**: 可在基础(阶段 2)后开始 → 可与 US1/US2/US3 并行

### 阶段 2 内的并行机会

所有 T005-T009 任务(代理结构实现)可并行运行：
- T005: 创建 ShadowTLS 结构体
- T006: 实现 Addr() 方法
- T007: 实现 Proto() 方法
- T009: 创建 New() 工厂函数

### 阶段 3 内的并行机会

所有测试可并行运行(T013-T015)，然后实施任务串行进行。

### 阶段 7 的并行优化

T052-T058 的所有优化任务可并行运行，T055-T058 的测试运行可并行进行。

---

## 并行执行示例

### 基础阶段(T005-T009)的并行执行

```bash
# 并行执行所有代理框架任务：
任务 T005: 创建 ShadowTLS 结构体
任务 T006: 实现 Addr() 方法
任务 T007: 实现 Proto() 方法
任务 T009: 创建 New() 工厂函数
# 上述任务可同时进行，因为它们修改不同的方法/部分

# 串行执行配置相关任务(依赖于代理结构完成)：
任务 T008: 添加 proto.ShadowTLS 常量
任务 T010: 实现 ShadowsocksConfig.Validate()
任务 T011: 实现协议选择逻辑
任务 T012: 配置加载逻辑
```

### 用户故事 1 的并行执行

```bash
# 第一步：并行编写所有测试
任务 T013: ShadowTLSPluginOpts 验证测试
任务 T014: ShadowsocksConfig 插件验证测试
任务 T015: 代理创建单元测试

# 第二步：确保测试失败(TDD 原则)

# 第三步：实施(可部分并行)
任务 T018-T019: 完善 New() 函数和协议选择逻辑
任务 T020-T022: 编写和运行集成测试
```

### 用户故事 2 和用户故事 3 的并行执行

基础(阶段 2)完成后：
- 开发者 A: 专注 US2(阶段 4) - 连接建立和数据转发
- 开发者 B: 专注 US3(阶段 5) - 加密方式支持

两个故事可完全独立进行，因为它们修改不同的文件：
- US2: 主要在 protocol.go, connection.go, DialContext()
- US3: 主要在加密方式验证和初始化逻辑

---

## 实施策略

### 仅 MVP(仅用户故事 1 + 基础连接)

**目标**: 快速交付最小可行产品 → 系统能识别和创建 ShadowTLS 代理，建立基本 TCP 连接

**执行顺序**:
1. ✅ 完成阶段 1: 设置(T001-T004)
2. ✅ 完成阶段 2: 基础(T005-T012) [2-3 小时]
3. ✅ 完成阶段 3: 用户故事 1 [1-2 小时]
4. ✅ 完成阶段 4: 用户故事 2 [2-3 小时] ← 添加基本连接支持
5. ✅ 部分完成阶段 7: 测试和基本文档 [1 小时]
6. 停止并验证: 独立测试用户故事 1 和 2
7. 如准备好则部署/演示 **[总耗时: 6-9 小时]**

### 增量交付(完整功能)

**目标**: 逐步交付完整功能，每个迭代都是可部署的

**迭代 1 - MVP**:
- 完成: 设置 + 基础 + US1 + US2 基础连接
- 部署: 系统能够识别和创建 ShadowTLS 代理，建立 TCP 连接
- 时间: 6-9 小时

**迭代 2 - US3**:
- 添加: US3(多种加密方式支持)
- 部署: 用户可选择不同加密方式
- 时间: 2-3 小时

**迭代 3 - US4**:
- 添加: US4(完善错误处理)
- 部署: 用户获得清晰的错误消息
- 时间: 2-3 小时

**迭代 4 - 完善**:
- 添加: 性能优化、文档完善、测试增强
- 部署: 生产就绪
- 时间: 3-4 小时

**总耗时**: 13-19 小时

### 团队并行策略(多开发者)

**有多个开发人员时**:

1. **所有人一起完成**：设置(阶段 1) + 基础(阶段 2) [2-3 小时]
   - 确保统一的项目结构和配置

2. **基础完成后分工**：
   - 开发者 A: 用户故事 1(配置解析) + 用户故事 2(连接)
   - 开发者 B: 用户故事 3(加密方式) + 用户故事 4(错误处理)
   - 并行进行 [4-6 小时]

3. **最后所有人一起完成**：完善阶段(阶段 7) [2-3 小时]

**总耗时**: 8-12 小时(多个开发者并行)

---

## 任务计数与范围

### 总体统计

- **总任务数**: 67 个任务(T001-T067)
- **并行标记[P]任务**: 43 个(64%)
- **用户故事任务**: 44 个(66%)
- **测试任务**: 16 个(24%)
- **实施任务**: 34 个(51%)
- **完善任务**: 16 个(24%)

### 按阶段分布

| 阶段 | 任务数 | 描述 |
|------|--------|------|
| 阶段 1: 设置 | 4 | 项目初始化 |
| 阶段 2: 基础 | 8 | 代理框架和协议选择 |
| 阶段 3: US1 | 10 | 配置解析(MVP) |
| 阶段 4: US2 | 10 | 连接建立和转发(MVP) |
| 阶段 5: US3 | 7 | 加密方式支持 |
| 阶段 6: US4 | 12 | 错误处理 |
| 阶段 7: 完善 | 16 | 优化和文档 |

### 按用户故事分布

| 故事 | 优先级 | 任务数 | 测试数 | 实施数 |
|------|--------|--------|--------|--------|
| US1 | P1 | 10 | 3 | 7 |
| US2 | P1 | 10 | 2 | 8 |
| US3 | P2 | 7 | 2 | 5 |
| US4 | P2 | 12 | 4 | 8 |

### 并行机会

- **阶段 1**: 0 个并行(顺序初始化)
- **阶段 2**: 5 个并行机会(T005-T009 的 4 个可并行)
- **阶段 3**: 3 个并行机会(测试可并行)
- **阶段 4**: 2 个并行机会(TLS+认证+连接可部分并行)
- **阶段 5**: 2 个并行机会(加密方式测试可并行)
- **阶段 6**: 4 个并行机会(多种错误场景测试可并行)
- **阶段 7**: 8 个并行机会(优化任务大多可并行)

**最大并行度**: 约 8 个任务(在阶段 7)

---

## 成功标准与验收

### 功能完成标准

**用户故事 1 - 配置解析** ✅
- [ ] 配置验证在加载时检测所有必需参数缺失
- [ ] 系统正确识别 `plugin: shadow-tls` 并创建 ShadowTLS 代理
- [ ] 不带 plugin 的配置创建标准 Shadowsocks 代理

**用户故事 2 - TCP 连接** ✅
- [ ] 通过 ShadowTLS 代理建立 TCP 连���(< 3 秒)
- [ ] 数据正确转发通过代理
- [ ] 目标不可达时返回适当错误

**用户故事 3 - 加密方式** ✅
- [ ] 支持所有主流加密方式(chacha20, aes-gcm 等)
- [ ] 不支持的加密方式返回错误

**用户故事 4 - 错误处理** ✅
- [ ] 所有错误返回清晰的错误消息
- [ ] 错误消息帮助用户定位问题

### 性能标准

- [ ] SC-001: 初始化延迟 < 1 秒 ✅ (T067)
- [ ] SC-002: 连接建立 < 3 秒 ✅ (T031, T056)
- [ ] SC-003: 100+ 并发连接 ✅ (T057)

### 质量标准

- [ ] SC-004: 配置验证 100% ✅ (T013-T015)
- [ ] SC-005: 协议兼容性通过 ✅ (T030, T037-T038)
- [ ] SC-006: 95% 错误定位成功 ✅ (T048-T051)
- [ ] SC-007: 数据完整性 100% ✅ (T030)
- [ ] SC-008: 100ms 资源释放 ✅ (T058)

---

## 注意事项

### 实施原则

- ✅ [P] 任务 = 不同文件，无依赖关系
- ✅ [Story] 标签将任务映射到特定用户故事以实现可追踪性
- ✅ 每个用户故事应该独立可完成和可测试
- ✅ 测试优先(TDD): 先编写测试，确保它们失败，再实施
- ✅ 逐个任务提交: 每个任务或逻辑组后提交一次
- ✅ 在每个检查点停止以独立验证故事

### 风险和缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| TLS 握手实现不当 | 协议不兼容 | 参考 sing-box 实现，编写充分测试 |
| 认证失败 | 无法连接 | 严格遵循协议文档，集成测试 |
| 性能不达标 | 用户体验差 | TLS 会话缓存，连接池优化 |
| 资源泄漏 | 内存溢出 | 完善的关闭逻辑，内存测试 |
| 错误消息不清晰 | 用户困惑 | 分层错误处理，充分的错误测试 |

### 避免的错误

- ❌ 跳过测试 → 必须先编写测试
- ❌ 同文件并行修改 → 导致冲突
- ❌ 跨故事依赖 → 破坏独立性
- ❌ 模糊的任务描述 → LLM 无法完成
- ❌ 忽视性能要求 → 不满足用户期望
- ❌ 弱错误处理 → 用户体验差

---

## 下一步

1. **验证任务列表**: 确保所有 67 个任务都清晰且可执行
2. **选择实施策略**:
   - MVP(6-9 小时): 仅 US1 + US2 基础
   - 完整(13-19 小时): 所有用户故事
   - 团队并行(8-12 小时): 多开发者
3. **开始执行**: 从阶段 1 开始，逐步完成各阶段
4. **监控进度**: 在每个检查点验证成果

---

**创建时间**: 2025-12-20
**规范**: [spec.md](./spec.md) | **计划**: [plan.md](./plan.md) | **数据模型**: [data-model.md](./data-model.md)
**快速开始**: [quickstart.md](./quickstart.md)
