# 无 SNI TLS 放行规则引擎实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 无 SNI TLS 连接携带 IP 上下文咨询规则引擎（IP/geoip 规则生效），toAliang 命中硬排除降级 direct（空名 MITM 必断链不可达），无规则命中维持现状 direct；MITM/镜像零误触。

**Architecture:** 单点改动 `processor/tcp/handler.go:636` 提前直连段——复用既有 `DetermineRouteWithContext`（`:636` 与正常路径 `:643` 之间零中间状态，评审已证实结构性安全）；toAliang 降级抽为纯函数便于测试；安全注释 `:518-521` 同步改写；文档三盲区改两盲区。

**Tech Stack:** Go 1.25、标准库 testing（processor/tcp 有 TestMain 隔离）。

**语义权威：** spec `docs/superpowers/specs/2026-10-14-nosni-tls-routing-design.md`（已评审通过）。**关键事实**：无 SNI 时 `mitmedSNI` 必为空串（`:657-661` 不可达）；`EffectiveDefaultProxy` 硬编码 `"direct"`（`processor/config/types.go:1035-1042`）→ 引擎回退恒 direct、**不存在 socks fallback**；mirror 名称硬门四重保护。

**约定：** 全中文提交「新增：/修复：/重构：」+ `Co-Authored-By: Claude Code <noreply@anthropic.com>` 页脚；标准库 testing；`go test ./...` 预存 flake（services 包）单包复跑判定；`test.panic panic=boom` 日志是既有 panic 恢复测试的正常输出。

**⚠️ 并行会话警示：** `../alianggate-geoip`（feat/geoip-enable）由另一会话执行中——**绝不进入该 worktree、绝不改动其文件**。本计划全程只在 `../alianggate-nosni` 工作。

---

### Task 0: worktree 环境准备

**Files:** 无代码改动

- [ ] **Step 1: 创建 worktree 分支**（主工作区执行）

```bash
git stash -u   # ⚠️ 确认无并行会话同时 stash；pop 失败须立即恢复
git worktree add ../alianggate-nosni -b feat/nosni-tls-routing
git stash pop
```

若分支已存在（并行撞车）：停止执行，报告协调者/用户，勿强推勿删除。

- [ ] **Step 2: 拷贝 spec 与本计划并提交**

```bash
mkdir -p ../alianggate-nosni/docs/superpowers/specs ../alianggate-nosni/docs/superpowers/plans
cp docs/superpowers/specs/2026-10-14-nosni-tls-routing-design.md ../alianggate-nosni/docs/superpowers/specs/
cp docs/superpowers/plans/2026-10-14-nosni-tls-routing.md ../alianggate-nosni/docs/superpowers/plans/
cd ../alianggate-nosni
git add docs/superpowers/
git commit -m "$(cat <<'EOF'
新增：无 SNI TLS 放行规则引擎设计文档与实现计划

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 1: toAliang 降级纯函数 + 测试

**Files:**
- Modify: `processor/tcp/handler.go`（新增纯函数，放在 resolveTLSRoute 附近）
- Test: `processor/tcp/handler_test.go`（新建或追加——先看包内既有测试文件名）

- [ ] **Step 1: 写失败测试**

```go
func TestDowngradeNoSNIAliangRoute(t *testing.T) {
	cases := []struct {
		name string
		in   ProxyRoute
		want ProxyRoute
	}{
		// tcp 包仅三常量（interfaces.go:157-161）；引擎 RouteToSocks 在 tls.go:328-329
		// 已映射为 RouteToLocalProxy——socks 场景锁 LocalProxy 即可
		{"toaliang-downgraded", RouteToALiang, RouteDirect},
		{"localproxy-kept", RouteToLocalProxy, RouteToLocalProxy},
		{"direct-kept", RouteDirect, RouteDirect},
		{"unknown-kept", ProxyRoute(99), ProxyRoute(99)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := downgradeNoSNIAliangRoute(tc.in); got != tc.want {
				t.Fatalf("downgradeNoSNIAliangRoute(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
```

**类型事实（评审者已钉死）**：`DetermineRouteWithContext`（`tls.go:277`）签名 `func (h *DefaultTLSHandler) DetermineRouteWithContext(metadata *M.Metadata) (ProxyRoute, bool)`——第二返回值是 bool（`requiresSNI`），**无 error**；`ProxyRoute` 是 int 枚举（带 String()）；常量 `RouteToALiang`/`RouteToLocalProxy`/`RouteDirect` 在 `processor/tcp/interfaces.go:157-161`。

- [ ] **Step 2: 确认失败**

Run: `go test ./processor/tcp/ -run TestDowngradeNoSNIAliangRoute -v`
Expected: 编译失败（函数未定义）

- [ ] **Step 3: 实现**

```go
// downgradeNoSNIAliangRoute 无 SNI 连接命中 toAliang 时硬排除降级 direct：
// 空 HostName 会使 mitmedSNI 为空串，PerformMITM 给空名签证书必断链
// （spec: docs/superpowers/specs/2026-10-14-nosni-tls-routing-design.md §3.1）。
// shouldForceAliangRoute 特判（本地代理端口+回环）来源的命中同样经此降级并打日志。
func downgradeNoSNIAliangRoute(route ProxyRoute) ProxyRoute {
	if route == RouteToALiang {
		logger.Debug("[TLS] no-SNI connection hit toAliang rule; downgraded to direct (empty-name MITM would break)")
		return RouteDirect
	}
	return route
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/tcp/ -run TestDowngradeNoSNIAliangRoute -v -count=1 && go build ./...`
Expected: 全绿

- [ ] **Step 5: 提交**

```bash
git add processor/tcp/
git commit -m "$(cat <<'EOF'
新增：无 SNI 连接 toAliang 命中降级纯函数（空名 MITM 断链防护）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 放行接线 + 安全注释改写

**Files:**
- Modify: `processor/tcp/handler.go`（`:636` 提前直连段；`:518-521` 注释）

- [ ] **Step 1: 基线**

Run: `go test ./processor/tcp/ -count=1`
Expected: 全绿（记录基线）

- [ ] **Step 2: 接线**——把 `:636` 一带（STEP 2 末尾的 `if metadata.HostName == "" { return h.resolveTLSRoute(..., RouteDirect, ...) }`）替换为：

```go
		if metadata.HostName == "" {
			// 无 SNI：不做 MITM（证书错配防护不变），但携带 IP 咨询规则引擎
			// 让 IP/geoip 规则生效（spec §3.1）；toAliang 命中降级 direct。
			route, _ := h.tlsHandler.DetermineRouteWithContext(metadata) // (ProxyRoute, requiresSNI bool)；引擎失败内部已回退 default direct（tls.go:307-322）
			route = downgradeNoSNIAliangRoute(route)
			return h.resolveTLSRoute(sessionCtx, connectCtx, originConn, metadata, route, wrapped, "")
		}
```

（唯一合法写法：第二返回值是 bool 非 error，`route, _ :=` 与正常路径 `:643` 及 `:512` 一致；引擎失败处理在 `decideRouteWithRoutingEngine` 内部——Warn + `defaultFallbackRoute` 恒 direct。）

- [ ] **Step 3: 注释改写**（`:518-521` 函数头注释，保留 MITM 防护语义、追加分流语义）

```go
// handleTLS processes TLS connections (port 443).
// Transparent TLS interception only trusts identifiers observed on the current
// connection. If we can't read an explicit hostname or SNI, we bypass MITM
// (certificate/HTTP-2 mismatch protection on shared IPs) but still consult the
// routing engine with the destination IP so ip/geoip rules can steer the flow;
// toAliang hits are downgraded to direct (empty-name MITM would break).
```

- [ ] **Step 4: 回归**

Run: `go test ./processor/tcp/ -v -count=1 && go build ./... && go vet ./processor/tcp/`
Expected: 全绿

- [ ] **Step 5: 提交**

```bash
git add processor/tcp/handler.go
git commit -m "$(cat <<'EOF'
新增：无 SNI TLS 连接咨询规则引擎（IP/geoip 规则生效，toAliang 硬排除降级）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 文档——三盲区改两盲区

**Files:**
- Modify: `README.md:167` 附近、`README.zh-CN.md:163` 附近、`docs/tutorials/zh_CN/usage-guide.md:692` 附近、`docs/tutorials/en/usage-guide.md:397` 附近（生效范围段）
- Modify: `docs/tutorials/zh_CN/usage-guide.import.json` + `usage-guide.mdx`（`python3 docs/tutorials/scripts/md_to_mdx.py` 重生成）

**内容要点**：
- 生效范围改为「两盲区」：UDP 恒直连、本地 HTTP 代理入站（IP 条件不生效）——**无 SNI TLS 已支持携带目的 IP 咨询规则引擎**（IP/geoip 规则生效；域名规则对无 SNI 流量天然不命中）
- 无 SNI 命中 toAliang 类规则自动降级直连（空名 MITM 断链防护；含 `shouldForceAliangRoute` 特判来源，日志会注明）；deny 规则退化 direct（`EffectiveDefaultProxy` 硬编码，无 socks fallback）
- 行为变化提示：写 IP/geoip 规则的用户，无 SNI 流量开始按规则分流（发布说明素材）

- [ ] **Step 1: 更新四处 + 重生成 mdx/import.json**
- [ ] **Step 2: 提交**

```bash
git add README.md README.zh-CN.md docs/
git commit -m "$(cat <<'EOF'
新增：无 SNI TLS 分流文档（生效范围三盲区改两盲区）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 全量验证与验收核对

- [ ] **Step 1: 全仓** `go build ./... && go vet ./... && go test ./... 2>&1 | grep -E "FAIL|panic" | head -5`（flake 守则同前）
- [ ] **Step 2: 分支盘点** `git log --oneline master..HEAD` + `git diff master..HEAD --stat | tail -3`
- [ ] **Step 3: 验收核对**（spec §7 六条，逐条给证据；其中「IP CONNECT 建连」「geoip 分流」两条以单元级 + 代码路径 trace 为证——handler 级真连接集成不在自动化范围，须在报告显式声明）
- [ ] **Step 4: 汇总报告**
