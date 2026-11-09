import { expect, test } from '@playwright/test';

// 组网设置页 e2e 冒烟（design §9 / 前端计划 Task 6）：
// 后端全部 route mock——本 spec 只验证「设置页能把组网卡真的渲染出来并走通
// 读三路 + 写回 mesh patch」这条链路，不验证 mesh 转发本身（那是后端集成测试）。
// 覆盖点：
//   ① 拓扑画布按 /api/mesh/status + customer.proxy_rules 渲染（本机居中/peer/徽标）
//   ② 点连线 → 节点对详情面板露出规则原文
//   ③ 离线兜底卡保存 → POST /api/config/customer 载荷含 mesh.fallback
//   ④ 规则编辑器接受 mesh 尾段（Task 4 修复的裸逗号缺陷在生产构建中同样成立）
const CUSTOMER = {
  proxy: {
    enable: false,
    type: 'socks5',
    server: '',
    username: '',
    password: '',
    private_key: '',
    key_passphrase: '',
    pool_size: 0,
    jump_servers: []
  },
  ai_rules: {},
  proxy_rules: ['openai.com', 'x.com,mesh:dev-b', 'y.com,mesh:dev-b,direct', 'z.com,mesh:dev-c'],
  mesh: {
    enable: true,
    forward_enable: false,
    max_hops: 5,
    peers: [
      { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true },
      { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
    ],
    fallback: { enable: false, classes: [], addr: '' }
  }
};

const MESH_STATUS = {
  device_id: 'dev-a',
  enable: true,
  forward_enable: false,
  lan_listen_port: 56441,
  peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }],
  lan_peers: []
};

const LAN_PEERS = { peers: [{ device_id: 'dev-b', mesh_port: 56441, cert_fingerprint: '', last_seen_unix: 1700000000 }] };

const ok = (data) => ({ contentType: 'application/json', body: JSON.stringify({ code: 0, msg: '', data }) });

// 仪表盘/设置页共用的最小环境桩：EventSource + WebSocket 不真连（否则
// 页面挂在连接态，设置页可达性受影响），语言固定 zh 以断言中文文案。
async function installPageStub(page) {
  await page.addInitScript(() => {
    window.localStorage.setItem('aliang-lang', 'zh');
    window.EventSource = class {
      constructor() { setTimeout(() => this.onopen?.(), 0); }
      close() {}
    };
    // 最小 WebSocket：readyState 常量齐全（应用以 WebSocket.OPEN 做就绪判定），
    // 连接即开、发送静默吞掉——本 spec 不关心 WS 内容。
    window.WebSocket = class {
      static OPEN = 1; static CONNECTING = 0; static CLOSING = 2; static CLOSED = 3;
      constructor() {
        this.readyState = 1;
        setTimeout(() => this.onopen?.(), 0);
      }
      send() {}
      close() {}
      addEventListener() {}
      removeEventListener() {}
    };
  });
}

async function stubApi(page, { onSave } = {}) {
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === '/api/auth/session') {
      await route.fulfill(ok({
        type: 'session_snapshot',
        instance_id: 'e2e-mesh',
        revision: 1,
        state: 'active',
        user: { id: 7, username: 'alice', email: 'alice@example.com' }
      }));
      return;
    }
    if (path === '/api/config/customer' && route.request().method() === 'POST') {
      const body = route.request().postDataJSON();
      onSave?.(body);
      await route.fulfill(ok({ customer: body?.customer }));
      return;
    }
    if (path === '/api/config/customer') {
      await route.fulfill(ok({ customer: CUSTOMER, version: 'v-e2e' }));
      return;
    }
    if (path === '/api/config/customer/providers') {
      await route.fulfill(ok({ providers: [] }));
      return;
    }
    if (path === '/api/mesh/status') {
      await route.fulfill(ok(MESH_STATUS));
      return;
    }
    if (path === '/api/mesh/lan_peers') {
      await route.fulfill(ok(LAN_PEERS));
      return;
    }
    if (path === '/api/agent/status') {
      await route.fulfill(ok({ runtime: { online: true } }));
      return;
    }
    await route.fulfill(ok({}));
  });
}

// 只走前端路由（不经侧栏点击）：模块级 currentPage 由 useNavigation 驱动。
async function openSettings(page) {
  await page.goto('/');
  await page.evaluate(async () => {
    const { useNavigation } = await import('/src/composables/useNavigation.js');
    useNavigation().showSettings();
  });
  await expect(page.getByText('节点组网')).toBeVisible();
}

test('组网卡渲染：状态条在线 + 拓扑画布（本机居中/peer/规则数徽标）+ 点连线看节点对详情', async ({ page }) => {
  await installPageStub(page);
  await stubApi(page);
  await openSettings(page);

  // ① 状态条：runtime 在线 + 已配置 → 在线绿态
  const pill = page.locator('[data-mesh-status-pill]');
  await expect(pill).toContainText('在线');

  // ② 拓扑画布：本机 dev-a + 配置的 peer dev-b/dev-c 都渲染
  const canvas = page.locator('svg[data-mesh-canvas]');
  await expect(canvas).toBeVisible();
  await expect(page.locator('[data-mesh-node="dev-a"]')).toBeVisible();
  await expect(page.locator('[data-mesh-node="dev-b"]')).toBeVisible();
  await expect(page.locator('[data-mesh-node="dev-c"]')).toBeVisible();
  // 本机节点显示 LAN 监听口
  await expect(page.locator('[data-mesh-node="dev-a"]')).toContainText(':56441');

  // ③ 徽标 = 该 peer 的 mesh 规则条数（dev-b 两条，含策略段形式的 y.com,mesh:dev-b,direct）
  await expect(page.locator('[data-mesh-badge="dev-b"]')).toContainText('2');
  await expect(page.locator('[data-mesh-badge="dev-c"]')).toContainText('1');

  // ④ 点徽标（可见的圆形命中区）→ 右栏节点对详情，规则原文按序露出。
  //    连线的可见线只有 2px 且可能完全轴向（几何包围盒高/宽为 0），Playwright
  //    视作不可见；真实命中区是叠在同位置上的 14px 透明线（见 MeshSettings.vue），
  //    其可达性由 vitest 断言，e2e 走徽标这一可见目标。
  await page.locator('[data-mesh-badge="dev-b"]').click();
  const detail = page.locator('[data-mesh-detail]');
  await expect(detail).toContainText('节点对详情');
  await expect(detail).toContainText('dev-b');
  const chips = detail.locator('[data-mesh-rule-chip]');
  await expect(chips).toHaveCount(2);
  await expect(chips.nth(0)).toHaveText('x.com,mesh:dev-b');
  await expect(chips.nth(1)).toHaveText('y.com,mesh:dev-b,direct');

  // ⑤ 点节点 → 节点详情（互斥:节点对面板让位）
  await page.locator('[data-mesh-node="dev-c"] text').first().click();
  await expect(detail).toContainText('节点详情');
  await expect(detail).toContainText('dev-c');
});

test('离线兜底保存：POST /api/config/customer 载荷含 mesh.fallback（差异 patch，不带未变段）', async ({ page }) => {
  let savedBody = null;
  await installPageStub(page);
  await stubApi(page, { onSave: (body) => { savedBody = body; } });
  await openSettings(page);

  const fallback = page.locator('[data-mesh-fallback]');
  await fallback.locator('[data-field="meshFallbackEnable"]').check({ force: true });
  await fallback.locator('[data-field="meshFallbackAddr"]').fill('192.168.1.20:56441');
  await fallback.locator('[data-field="meshClassAi"]').check({ force: true });

  await page.locator('button[data-mesh-save]').click();
  await expect.poll(() => savedBody).not.toBeNull();

  expect(savedBody.customer.mesh.fallback).toEqual({
    enable: true,
    addr: '192.168.1.20:56441',
    classes: ['ai']
  });
  // 载荷是「与当前配置的差异」而非全量：本卡没动的顶层段（proxy_rules）与
  // 未变的 mesh 开关（enable/forward_enable/max_hops/peers）都不出现在 patch 里
  // ——父层按顶层段浅合并，多带的键反而会把别人的段覆盖成陈旧值。
  expect(savedBody.customer.proxy_rules).toBeUndefined();
  expect(savedBody.customer.mesh.enable).toBeUndefined();
  expect(savedBody.customer.mesh.forward_enable).toBeUndefined();
  expect(savedBody.customer.mesh.peers).toBeUndefined();
});

test('规则编辑器接受 mesh 尾段：裸逗号形态无红错，非法形态给本地提示', async ({ page }) => {
  await installPageStub(page);
  await stubApi(page);
  await openSettings(page);

  const rulesInput = page.getByPlaceholder(/每行一条/);
  await expect(rulesInput).toBeVisible();

  // 现有配置里的 mesh 条目（含策略段形式）不得报错
  await expect(page.getByText('组网尾段格式非法')).toHaveCount(0);

  // 尾段形态非法（空 deviceID）→ 本地提示（后端会整条静默跳过）
  await rulesInput.fill('x.com,mesh:\ny.com,mesh:dev-b');
  await expect(page.getByText('组网尾段格式非法')).toBeVisible();

  // 修好 → 提示消失
  await rulesInput.fill('x.com,mesh:dev-b\ny.com,mesh:dev-b,deny');
  await expect(page.getByText('组网尾段格式非法')).toHaveCount(0);
});
