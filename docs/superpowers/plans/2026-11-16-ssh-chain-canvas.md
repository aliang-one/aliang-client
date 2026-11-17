# SSH 出口链路 canvas 拓扑化 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用零依赖 SVG 拓扑画布(本机→跳板N→出口节点链 + 并行连接泳道)替换 RulesSettings 与 LogsSettings 两处 SSH 出口链路平铺列表,后端补 `jump_addrs` 一个字段。

**Architecture:** 新建共享组件 `SshChainCanvas.vue`(对齐 mesh 画布范式),两处页面接入;泳道状态推导从两处页面级重复实现收敛为组件内单份;`SSH` 实例留存跳板地址并经 `SSHChainStatus` 透传。

**Tech Stack:** Vue 3(页面既有 Options/Composition 混用,组件跟随宿主页面惯例)、零依赖 SVG、Go(`outbound/proxy/ssh` + `outbound/registry`)、vitest + jsdom(仓内无 `@vue/test-utils`)、`go test`。

**Spec:** `docs/superpowers/specs/2026-11-16-ssh-chain-canvas-design.md`

---

## 执行前置(只做一次)

- [ ] **P0: 建 worktree**(用户工作流惯例:所有开发一律 worktree 切分支)

```bash
git -C /Users/mac/MyProgram/GoProgram/nursor/alianggate worktree add .worktrees/ssh-chain-canvas -b feat/ssh-chain-canvas
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/ssh-chain-canvas
```

新 worktree 构建两坑(记忆 [[transfer-worktree-build-gotchas]]):首次 build 若缺 dist 从主仓 `cp -R app/website/dist app/website/`;gopls 误报一律无视,以 build/test 为准。

- [ ] **P1: 提交纪律**——标题全中文「新增:/修复:」,HEREDOC 多行,footer 与 co-author 按 CLAUDE.md;**绝不 push origin**;并行会话共仓,`git add` 只加本计划点名的文件,绝不 `add -A`。

---

### Task 1: Go——跳板地址留存 + `JumpAddrs()` + wire 透传

**Files:**
- Modify: `outbound/proxy/ssh/ssh.go`(SSH 结构体 ~38-44、New ~80-88、新增 JumpAddrs 方法于 JumpCount ~198 之后)
- Modify: `outbound/registry.go:310-315`(struct)、`outbound/registry.go:328-333`(透传)
- Test: `outbound/proxy/ssh/ssh_test.go`(新增 `TestSSHJumpAddrs`,置于 `TestSSHJumpCount` ~215 之后)
- Test: `outbound/registry_test.go`(已存在,package outbound;追加 `TestSSHChainStatusWireShape`)

- [ ] **Step 1.1: 写失败测试**(`ssh_test.go`,fixture 镜像 `TestSSHJumpCount` 的 `NodeFromAddrPort(..., "fu", "fp", ...)` 口径)

```go
// TestSSHJumpAddrs 钉跳板地址透出:路径序 host:port、副本隔离、无跳板=nil。
func TestSSHJumpAddrs(t *testing.T) {
	final := NodeFromAddrPort("127.0.0.1", 22, "fu", "fp", "", "")
	jump1 := NodeFromAddrPort("127.0.0.1", 2222, "ju", "jp", "", "")
	jump2 := NodeFromAddrPort("127.0.0.1", 2223, "ju2", "jp2", "", "")

	p, err := New(final, 1, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New(direct): %v", err)
	}
	if got := p.JumpAddrs(); got != nil {
		t.Fatalf("JumpAddrs(direct)=%v, want nil", got)
	}
	_ = p.Close()

	p, err = New(final, 1, []NodeConfig{jump1, jump2}, t.TempDir())
	if err != nil {
		t.Fatalf("New(2 jumps): %v", err)
	}
	defer func() { _ = p.Close() }()
	got := p.JumpAddrs()
	want := []string{"127.0.0.1:2222", "127.0.0.1:2223"}
	if len(got) != len(want) {
		t.Fatalf("JumpAddrs=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("JumpAddrs[%d]=%s, want %s", i, got[i], want[i])
		}
	}
	got[0] = "tampered"
	if p.JumpAddrs()[0] != want[0] {
		t.Fatalf("JumpAddrs 未做副本隔离")
	}
}
```

`registry_test.go`(文件已有 `testing` 与 `sshout` 别名 import,仅补 `bytes`/`encoding/json` 两个 import):

```go
// TestSSHChainStatusWireShape 钉 wire 形态:零值省键(addr/jumps/jump_addrs
// 均 omitempty)、配置态齐键——补 2026-11-15 遗留账「Jumps wire 无 Go 断言」。
func TestSSHChainStatusWireShape(t *testing.T) {
	empty, err := json.Marshal(SSHChainStatus{OK: false, Chains: []sshout.ChainStatus{}})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	for _, key := range []string{"addr", "jumps", "jump_addrs"} {
		if bytes.Contains(empty, []byte(`"`+key+`"`)) {
			t.Fatalf("零值应省略 %s: %s", key, empty)
		}
	}
	full, err := json.Marshal(SSHChainStatus{
		OK: true, Addr: "h:22", Jumps: 2,
		JumpAddrs: []string{"a:1", "b:2"}, Chains: []sshout.ChainStatus{},
	})
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	for _, key := range []string{`"addr":"h:22"`, `"jumps":2`, `"jump_addrs":["a:1","b:2"]`, `"chains"`} {
		if !bytes.Contains(full, []byte(key)) {
			t.Fatalf("配置态缺 %s: %s", key, full)
		}
	}
}
```

- [ ] **Step 1.2: 跑测试确认失败**

Run: `go test ./outbound/proxy/ssh/ -run TestSSHJumpAddrs -v && go test ./outbound/ -run TestSSHChainStatusWireShape -v`
Expected: FAIL(`p.JumpAddrs undefined` / `SSHChainStatus.JumpAddrs unknown field`)

- [ ] **Step 1.3: 实现**

`ssh.go` 结构体加字段与注释:

```go
type SSH struct {
	*proxy.Base

	pool      *pool
	closed    atomic.Bool
	jumpCount int      // 跳板跳数(0=直连出口);状态页路径行消费
	jumpAddrs []string // 跳板地址(路径序,hostPort 形态);拓扑画布节点消费
}
```

`New()` 内组装处(现 ~80-88)改为:

```go
	jumpAddrs := make([]string, 0, len(jumps))
	for _, j := range jumps {
		jumpAddrs = append(jumpAddrs, j.hostPort())
	}
	s := &SSH{
		Base:      &proxy.Base{Address: final.hostPort(), Protocol: proto.SSH},
		jumpCount: len(jumps),
		jumpAddrs: jumpAddrs,
	}
```

`JumpCount()` 之后新增方法:

```go
// JumpAddrs 返回跳板地址(路径序,host:port)。返回副本,调用方改写不伤
// 实例内部;无跳板返回 nil(omitempty 序列化直接省键)。
func (s *SSH) JumpAddrs() []string {
	if len(s.jumpAddrs) == 0 {
		return nil
	}
	out := make([]string, len(s.jumpAddrs))
	copy(out, s.jumpAddrs)
	return out
}
```

`registry.go` struct 加字段(排在 Jumps 与 Chains 之间,字段序即文档序):

```go
type SSHChainStatus struct {
	OK        bool                 `json:"ok"`
	Addr      string               `json:"addr,omitempty"`
	Jumps     int                  `json:"jumps,omitempty"`
	JumpAddrs []string             `json:"jump_addrs,omitempty"`
	Chains    []sshout.ChainStatus `json:"chains"`
}
```

`Registry.SSHChainStatus()` 返回值(~328-333)加一行 `JumpAddrs: ssh.JumpAddrs(),`。

- [ ] **Step 1.4: 跑测试确认通过**

Run: `go test ./outbound/proxy/ssh/ ./outbound/ -v -run 'TestSSHJumpAddrs|TestSSHChainStatusWireShape|TestSSHJumpCount'`
Expected: 全 PASS(含既有 TestSSHJumpCount 不回归)

- [ ] **Step 1.5: 全包回归 + gofmt**

Run: `go build ./... && go test ./outbound/... ./app/http/handlers/... && gofmt -l outbound app`
Expected: build 零错、测试全绿、gofmt 输出为空(已知预存 flake 见记忆 [[preexisting-race-flakes-transfer-2026-10]] 与 [[preexisting-test-flakes-subprocess-timeout]],单包复跑即绿,不许洗白)

- [ ] **Step 1.6: 提交**(只 add 本任务 4 个文件)

```bash
git add outbound/proxy/ssh/ssh.go outbound/proxy/ssh/ssh_test.go outbound/registry.go outbound/registry_test.go
git commit -m "$(cat <<'EOF'
新增:SSH 出口跳板地址透出——SSHChainStatus 增 jump_addrs 供拓扑画布消费

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: i18n——画布节点角色三键

**Files:**
- Modify: `app/website/src/i18n/zh.js:1115` 后(`status_sshPathConnectionOne` 之后追加)
- Modify: `app/website/src/i18n/en.js`(同键同位追加)

- [ ] **Step 2.1: 加键**(zh)

```js
  status_sshNodeLocal: '本机',
  status_sshNodeJump: '跳板 {index}',
  status_sshNodeExit: '出口',
```

en 同位(口径:简短名词):

```js
  status_sshNodeLocal: 'This device',
  status_sshNodeJump: 'Jump {index}',
  status_sshNodeExit: 'Exit',
```

- [ ] **Step 2.2: parity + 相关页测试**

Run: `cd app/website && npx vitest run src/i18n/i18n.test.js`
Expected: PASS(parity 测试自动覆盖新键双语在册)

- [ ] **Step 2.3: 提交**

```bash
git add app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
新增:SSH 链路拓扑画布节点角色文案键(本机/跳板/出口)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `SshChainCanvas.vue` 组件(TDD)

**Files:**
- Create: `app/website/src/components/settings/SshChainCanvas.vue`
- Test: `app/website/src/components/settings/SshChainCanvas.test.js`

**组件规格(来自 spec §5.2)**:props `ssh`(= `/api/status/summary` 的 `data.ssh`:`{ok, addr, jumps, jump_addrs?, chains}`);空态两分支(`ok=false`→`status_sshNotConfigured`;`ok=true` 且 chains 空→`rules_sshChainIdleHint`,均复用现有键不新增);泳道推导逐字搬迁 RulesSettings.vue:1392-1430 现有语义(`index=Number(chain.index||0)+1`;`remaining=Math.max(1,Math.round(ms/1000))`;backoff→琥珀/alive→绿/else→灰;activeText 仅 count>0);语义色常量绿 `#22c55e`/琥珀 `#f59e0b`/灰 `#94a3b8`(mesh §9.2 同款,SVG 不吃 Tailwind dark: 变体)。**测试 harness 按仓内范式写死:`vi.mock('../../i18n')` 令 `t` 原样回键(仓规:断言断 i18n 键,不硬编码文案);挂载用 `createApp`+`h`+jsdom(仓内无 `@vue/test-utils`);组件 `t` 经 `setup(){ const { t } = useI18n(); return { t }; }` 注线(RulesSettings.vue:1277-1286 同范式,仓内无全局 mixin)。**

- [ ] **Step 3.1: 写失败测试**(完整可落盘;断言全部键形态,与仓内九个测试文件同范式)

```js
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createApp, h } from 'vue';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage,node 环境无 DOM）,
// mock 掉后 t 原样回键,断言不硬编码文案(LogsSettings.test.js 同范式)。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k, p) => k }) }));

import SshChainCanvas from './SshChainCanvas.vue';

const mountedApps = [];
const mountCanvas = (ssh) => {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(SshChainCanvas, { ssh }) });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
};

afterEach(() => {
  mountedApps.forEach(({ app, el }) => {
    app.unmount();
    el.remove();
  });
  mountedApps.length = 0;
});

const configured = {
  ok: true,
  addr: 'exit.host:22',
  jumps: 1,
  jump_addrs: ['relay.host:2222'],
  chains: [
    { index: 0, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 12 },
    { index: 1, alive: false, backoff: true, backoffRemainingMs: 8000, activeChannels: 0 },
    { index: 2, alive: false, backoff: false, backoffRemainingMs: 0, activeChannels: 0 }
  ]
};

const svgText = (el) => el.querySelector('[data-ssh-chain-svg]')?.textContent || '';

describe('SshChainCanvas', () => {
  it('ok=false 渲染未配置空态(键=status_sshNotConfigured)', () => {
    const el = mountCanvas({ ok: false, chains: [] });
    expect(el.querySelector('[data-ssh-chain-empty]')).not.toBeNull();
    expect(el.querySelector('[data-ssh-chain-empty]').textContent).toContain('status_sshNotConfigured');
    expect(el.querySelector('[data-ssh-chain-svg]')).toBeNull();
  });

  it('ok=true 且 chains 空渲染瞬时空态(键=rules_sshChainIdleHint)', () => {
    const el = mountCanvas({ ok: true, addr: 'a:1', chains: [] });
    expect(el.querySelector('[data-ssh-chain-empty]').textContent).toContain('rules_sshChainIdleHint');
  });

  it('ssh prop 为 null 安全渲染空态不抛错', () => {
    const el = mountCanvas(null);
    expect(el.querySelector('[data-ssh-chain-empty]')).not.toBeNull();
  });

  it('节点链完整:本机/跳板/出口三节点齐,跳板带地址与出口带地址', () => {
    const el = mountCanvas(configured);
    const text = svgText(el);
    expect(text).toContain('status_sshNodeLocal');
    expect(text).toContain('status_sshNodeJump');
    expect(text).toContain('relay.host:2222');
    expect(text).toContain('status_sshNodeExit');
    expect(text).toContain('exit.host:22');
  });

  it('jump_addrs 缺失(旧 agent)降级:跳板节点仍在但不显地址', () => {
    const el = mountCanvas({ ...configured, jump_addrs: undefined });
    const text = svgText(el);
    expect(text).toContain('status_sshNodeJump');
    expect(text).not.toContain('relay.host:2222');
  });

  it('泳道三态键齐且语义色正确:存活绿/退避琥珀/空闲灰;活跃通道键仅>0 显', () => {
    const el = mountCanvas(configured);
    const lanes = el.querySelector('[data-ssh-chain-lanes]').textContent;
    expect(lanes).toContain('status_sshChainAlive');
    expect(lanes).toContain('status_sshChainBackoffIn');
    expect(lanes).toContain('status_sshChainIdle');
    expect(lanes).toContain('status_sshChainLabel');
    expect(lanes).toContain('status_sshChainActive');
    const svg = el.querySelector('[data-ssh-chain-svg]').outerHTML;
    expect(svg).toContain('#22c55e');
    expect(svg).toContain('#f59e0b');
    expect(svg).toContain('#94a3b8');
  });
});
```

- [ ] **Step 3.2: 跑测试确认失败**

Run: `cd app/website && npx vitest run src/components/settings/SshChainCanvas.test.js`
Expected: FAIL(组件文件不存在)

- [ ] **Step 3.3: 实现组件**(注意 `useI18n` 注线是硬要求——仓内无全局 mixin,漏注线则全部 computed 抛 `this.t is not a function`)

```vue
<template>
  <section data-ssh-chain-canvas class="space-y-2">
    <div
      v-if="emptyReason"
      class="rounded-xl border border-dashed border-slate-300 bg-slate-50/60 px-6 py-8 text-center text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40"
      data-ssh-chain-empty
    >
      {{ emptyReason }}
    </div>
    <svg
      v-else
      class="w-full rounded-xl border border-slate-200 bg-slate-50/40 dark:border-slate-800 dark:bg-slate-900/40"
      role="img"
      :viewBox="viewBox"
      data-ssh-chain-svg
    >
      <g data-ssh-chain-edges>
        <line
          v-for="(edge, i) in edges"
          :key="`edge-${i}`"
          :x1="edge.x1"
          :y1="42"
          :x2="edge.x2"
          :y2="42"
          stroke="#94a3b8"
          stroke-width="2"
        />
      </g>
      <g data-ssh-chain-nodes>
        <g v-for="(node, i) in nodeLayout" :key="`node-${i}`">
          <rect
            :x="node.x"
            y="16"
            width="130"
            height="52"
            rx="10"
            fill="#f8fafc"
            stroke="#cbd5e1"
            stroke-width="1.5"
          />
          <text :x="node.cx" :y="node.addr ? 36 : 46" text-anchor="middle" font-size="13" font-weight="600" fill="#0f172a">
            {{ node.label }}
          </text>
          <text v-if="node.addr" :x="node.cx" y="56" text-anchor="middle" font-size="11" fill="#64748b">
            {{ node.addr }}
          </text>
        </g>
      </g>
      <g data-ssh-chain-lanes>
        <g v-for="(lane, i) in lanes" :key="`lane-${i}`">
          <circle :cx="26" :cy="laneCy(i)" r="5" :fill="lane.color" />
          <text :x="40" :y="laneCy(i) + 4" font-size="12" font-weight="500" fill="#334155">{{ lane.label }}</text>
          <text x="200" :y="laneCy(i) + 4" font-size="12" :fill="lane.color">{{ lane.stateText }}</text>
          <text v-if="lane.activeText" x="614" :y="laneCy(i) + 4" text-anchor="end" font-size="11" fill="#64748b">
            {{ lane.activeText }}
          </text>
        </g>
      </g>
    </svg>
  </section>
</template>

<script>
import { useI18n } from '../../i18n';

// 语义色常量:SVG 属性不吃 Tailwind dark: 变体(mesh 画布 §9.2 同款先例)。
const LANE_COLOR_BACKOFF = '#f59e0b';
const LANE_COLOR_ALIVE = '#22c55e';
const LANE_COLOR_IDLE = '#94a3b8';

export default {
  name: 'SshChainCanvas',
  props: {
    // /api/status/summary 的 data.ssh 整体:{ ok, addr, jumps, jump_addrs?, chains }
    ssh: { type: Object, default: null }
  },
  setup() {
    const { t } = useI18n();
    return { t };
  },
  computed: {
    emptyReason() {
      if (!this.ssh || !this.ssh.ok) {
        return this.t('status_sshNotConfigured');
      }
      if (!Array.isArray(this.ssh.chains) || this.ssh.chains.length === 0) {
        return this.t('rules_sshChainIdleHint');
      }
      return '';
    },
    nodes() {
      const ssh = this.ssh || {};
      const list = [{ label: this.t('status_sshNodeLocal'), addr: '' }];
      const addrs = Array.isArray(ssh.jump_addrs) ? ssh.jump_addrs : [];
      const raw = Number(ssh.jumps);
      const jumpCount = Number.isFinite(raw) && raw > 0 ? raw : addrs.length;
      for (let i = 0; i < jumpCount; i += 1) {
        list.push({ label: this.t('status_sshNodeJump', { index: i + 1 }), addr: addrs[i] || '' });
      }
      list.push({ label: this.t('status_sshNodeExit'), addr: ssh.addr || '' });
      return list;
    },
    nodeLayout() {
      const count = this.nodes.length;
      const width = 130;
      const margin = 20;
      const span = 640 - margin * 2 - width;
      return this.nodes.map((node, i) => {
        const x = count === 1 ? margin : margin + (span * i) / (count - 1);
        return { ...node, x, cx: x + width / 2 };
      });
    },
    edges() {
      return this.nodeLayout.slice(1).map((node, i) => {
        const prev = this.nodeLayout[i];
        return { x1: prev.x + 130, x2: node.x };
      });
    },
    // 泳道推导:逐字搬迁自 RulesSettings.vue sshChains()(本批删除页级副本,
    // 收敛为组件内单份;见 spec §5.2 状态推导收敛)。
    lanes() {
      const chains = Array.isArray(this.ssh?.chains) ? this.ssh.chains : [];
      return chains.map((chain) => {
        const index = Number(chain.index || 0) + 1;
        const alive = Boolean(chain.alive);
        const backoff = Boolean(chain.backoff);
        const remaining = Math.max(1, Math.round(Number(chain.backoffRemainingMs || 0) / 1000));
        const activeCount = Number(chain.activeChannels || 0);
        let stateText;
        let color;
        if (backoff) {
          stateText = this.t('status_sshChainBackoffIn', { seconds: remaining });
          color = LANE_COLOR_BACKOFF;
        } else if (alive) {
          stateText = this.t('status_sshChainAlive');
          color = LANE_COLOR_ALIVE;
        } else {
          stateText = this.t('status_sshChainIdle');
          color = LANE_COLOR_IDLE;
        }
        return {
          label: this.t('status_sshChainLabel', { index }),
          color,
          stateText,
          activeText: activeCount > 0 ? this.t('status_sshChainActive', { count: activeCount }) : ''
        };
      });
    },
    viewHeight() {
      return 100 + this.lanes.length * 30 + 6;
    },
    viewBox() {
      return `0 0 640 ${this.viewHeight}`;
    }
  },
  methods: {
    laneCy(i) {
      return 100 + i * 30;
    }
  }
};
</script>
```

- [ ] **Step 3.4: 跑测试确认通过**

Run: `cd app/website && npx vitest run src/components/settings/SshChainCanvas.test.js`
Expected: 6/6 PASS

- [ ] **Step 3.5: 提交**

```bash
git add app/website/src/components/settings/SshChainCanvas.vue app/website/src/components/settings/SshChainCanvas.test.js
git commit -m "$(cat <<'EOF'
新增:SSH 出口链路 SVG 拓扑画布组件——节点链+并行连接泳道,泳道推导单份收敛

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: RulesSettings 接入(测试先行的锚点迁移)

**Files:**
- Modify: `app/website/src/components/settings/RulesSettings.vue`(灯区 390-407、data `sshChainsRaw` ~1339、`loadSshChains` ~1864-1868、**watcher 非 ssh 分支清结果 ~1558**、删 `sshChains` computed ~1391-1430)
- Test: `app/website/src/components/settings/RulesSettings.test.js`(~844 起经 `getStatusSummary` mock 喂完整 envelope 的 ssh 数据、~872 锚点断言)

- [ ] **Step 4.1: 改测试期望(先红)**——测试侧存储形态无需改造(mock 本就喂 `data.ssh` 完整对象,页面侧承接才是本批变更);给 ssh fixture 追加 `jumps: 1, jump_addrs: ['10.0.0.8-relay:2222']` 使页级画布断言能覆盖跳板地址;`data-ssh-chain-lights`/`data-ssh-chain-lights-empty` 锚点断言迁到 `[data-ssh-chain-canvas]` 存在 + 泳道文案经子组件 DOM 断言(键形态,同仓规)。跑 `npx vitest run src/components/settings/RulesSettings.test.js` 确认红(组件未接入)。

- [ ] **Step 4.2: 页面接入**

模板 390-407 整块(含 `v-else` 的 `rules_sshChainIdleHint` 行)替换为:

```html
              <SshChainCanvas :ssh="sshStatusRaw" />
```

脚本四处:
- `import SshChainCanvas from './SshChainCanvas.vue';` + `components` 选项注册(Options API,RulesSettings 现无 `components` 项,新增)。
- data(~1339):`sshChainsRaw: []` → `sshStatusRaw: null`。
- `loadSshChains()`(~1864-1868):`this.sshChainsRaw = Array.isArray(chains) ? chains : [];` → `this.sshStatusRaw = envelope?.data?.ssh ?? null;`
- **watcher `'form.proxy.type'` 非 ssh 分支(~1558)**:`this.sshChainsRaw = [];` → `this.sshStatusRaw = null;`(第三处写点,漏改则切换类型时旧快照残留不清)。
- 删除 `sshChains()` computed 整块(~1391-1430,逻辑已逐字入组件)。
- `startSshChainPolling`/`stopSshChainPolling` 不动(端点与 5s 频率不变)。

- [ ] **Step 4.3: 跑测试确认绿**

Run: `cd app/website && npx vitest run src/components/settings/RulesSettings.test.js`
Expected: 全 PASS;若有断言依赖已删 computed 的旧形态,按组件锚点修正断言体(不许删测试凑绿)

- [ ] **Step 4.4: 提交**

```bash
git add app/website/src/components/settings/RulesSettings.vue app/website/src/components/settings/RulesSettings.test.js
git commit -m "$(cat <<'EOF'
新增:出口设置页 SSH 链路区切换 SVG 拓扑画布——页级泳道推导删除收敛入组件

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: LogsSettings 接入 + 失效键清理

**Files:**
- Modify: `app/website/src/components/settings/LogsSettings.vue`(`data-ssh-path` 358-370 + `data-ssh-chains` 卡片网格 371-386(含闭合 `</div></template>`,整体替换;**外层未配置分支与 `<template v-else>` 结构保留不动**);删 `sshChainCount` ~564、`sshChainCards` computed ~638-672;`<script setup>` 下 `import SshChainCanvas` 即注册)
- Test: `app/website/src/components/settings/LogsSettings.test.js:160-254`(锚点迁移)
- Modify: `app/website/src/i18n/zh.js` / `en.js`(清理 `status_sshPathFinal/Jumps/Connections/ConnectionOne` 四失效键)

- [ ] **Step 5.1: 改测试期望(先红)**——~160-254 的 `data-ssh-path`/`data-ssh-chains` 断言迁到 `[data-ssh-chain-canvas]` + 泳道文案(键形态);**外层未配置分支的 `status_sshNotConfigured` 断言保留不动**。跑确认红。

- [ ] **Step 5.2: 页面接入**——`<template v-else>` 内部的路径行与连接卡整块(358-386)换成 `<SshChainCanvas :ssh="sshStatus" />`;删 `sshChainCount` 与 `sshChainCards`;轮询与 `sshStatus` 数据源不动。

- [ ] **Step 5.3: 确认绿 + 失效键清理**

Run: `cd app/website && npx vitest run src/components/settings/LogsSettings.test.js`
Expected: PASS

再全仓 grep 四键确认零引用后删除:

Run: `grep -rn "status_sshPathFinal\|status_sshPathJumps\|status_sshPathConnections\|status_sshPathConnectionOne" app/website/src/ --include="*.vue" --include="*.js" | grep -v i18n/`
Expected: 空(零引用才删;i18n/ 目录内的键定义在删后由 parity 测试兜底)

- [ ] **Step 5.4: parity + 提交**

Run: `cd app/website && npx vitest run src/i18n/i18n.test.js`
Expected: PASS

```bash
git add app/website/src/components/settings/LogsSettings.vue app/website/src/components/settings/LogsSettings.test.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
新增:状态页 SSH 出口卡切换 SVG 拓扑画布——路径行并入画布,清理四条失效键

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 全量验证收口

- [ ] **Step 6.0: 关 Task 1 质量评审 Minor 账**——扩展既有 `TestRegistryRefreshCustomerProxy_SSHWithJumps`(已构建带跳板的真实例),追加 `json.Marshal(reg.SSHChainStatus())` 断言含 `"jump_addrs"`(钉 registry 透传行,防删线不红)。跑绿后随 Task 6 收口提交或并入最近任务提交。

- [ ] **Step 6.0b: 关 Task 4/5 质量评审 Minor 账(措辞清扫微提交)**——(a) RulesSettings.vue:352-356 区头注释、:1721 分隔注释与 RulesSettings.test.js:829/831/832/885 注释及 describe/it 标题中残留的「状态灯」改为「拓扑画布/泳道」口径;(b) SshChainCanvas.vue:121 溯源注释「后续任务删除页级副本」改写为既成事实口径(两页副本均已删);(c) Task 5 评审:两处接入注释标点统一(LogsSettings.vue:358-359 半角 vs RulesSettings.vue:388-389 全角,取全角);(d) Task 5 评审:LogsSettings.test.js:264-270「旧后端无 ssh 字段」测试补一行 `[data-ssh-chain-canvas]` 为 null 的断言(防页面分支漏接时被组件空态假绿)。纯注释/标题/一行断言,随收口提交。

- [ ] **Step 6.1: Go 全量**

Run: `go build ./... && go test ./outbound/... ./app/http/handlers/... && gofmt -l outbound app`
Expected: 全绿;gofmt **口径修正**(Task 1 质量评审定谳):只要求本批改动文件干净——全仓另有 8 个文件为基线 455b867 预存格式漂移(含 outbound/proxy/ssh/pool.go),另立独立小账不在本批处理;预存 flake 按记忆两枚处置(单包复跑),不得静默跳过

- [ ] **Step 6.2: vitest 全量**

Run: `cd app/website && npx vitest run`
Expected: 全绿;总数应较基线上浮(新组件 6 测试),记忆口径 618→626 属正常加测试非缓存

- [ ] **Step 6.3: 收尾报告**——不推 origin、不出包;真机验收前置 = 0.196 重新启用出口链路(11-15 21:38 后已断,Mac 入口台账 active=0),起链后真机确认两处画布节点链与泳道状态;spec 与本计划文档保持未提交,是否入库听用户指令。
