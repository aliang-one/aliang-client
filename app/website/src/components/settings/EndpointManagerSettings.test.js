// LLM 端点管理卡组件测试。挂载用 createApp + jsdom 手法(仓内无
// @vue/test-utils,AgentMCPRegistrySettings.test.js 同范式);i18n 与 agentApi
// 必须 mock:前者避免 locale 依赖,后者阻止 mounted 的真实网络请求与 5s 轮询
// 打真接口。断言走 i18n 键名(mock t 原样回键),不硬编码文案。
// 重点守护 spec §17 不变量 7/8 的 UI 侧:api_key 掩码不回显/编辑留空=不改/
// builtin 禁改 base_url+auto_group/禁删。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../i18n', () => ({
  useI18n: () => ({
    t: (k, params = {}) => (params && Object.keys(params).length ? `${k} ${Object.values(params).join(' ')}` : k),
  }),
}));
vi.mock('../../services/agentApi', () => ({
  getEndpoints: vi.fn(),
  createEndpoint: vi.fn(),
  updateEndpoint: vi.fn(),
  deleteEndpoint: vi.fn(),
  testEndpoint: vi.fn(),
  fetchEndpointModels: vi.fn(),
  resetEndpointHealth: vi.fn(),
  setEndpointEgressKernel: vi.fn(),
  getEndpointPolicy: vi.fn(),
  setEndpointPolicy: vi.fn(),
}));
vi.mock('../../composables/useErrorDialog', () => ({
  showErrorDialog: vi.fn(),
}));

import { createApp, h } from 'vue';
import EndpointManagerSettings, {
  endpointHealthBadge,
  endpointToneClass,
} from './EndpointManagerSettings.vue';
import { showErrorDialog } from '../../composables/useErrorDialog';
import { formatClockTime } from '../../utils/endpointSettings';
import {
  createEndpoint,
  deleteEndpoint,
  fetchEndpointModels,
  getEndpointPolicy,
  getEndpoints,
  resetEndpointHealth,
  setEndpointEgressKernel,
  setEndpointPolicy,
  testEndpoint,
  updateEndpoint,
} from '../../services/agentApi';

const endpointsData = () => [
  {
    id: 'builtin-aliang',
    name: 'Aliang 隧道（内置）',
    base_url: '',
    api_key: '',
    protocol_anthropic: true,
    protocol_openai: true,
    auto_group: true,
    priority: 0,
    max_concurrency: 0,
    models: '[]',
    enabled: true,
    builtin: true,
    health_status: 'ok',
    last_error: '',
    inflight: 2,
    egress_kernel: false,
    last_used_at: 0,
    total_requests: 0,
    failed_requests: 0,
    cooldown_until: 0,
    rate_limited_until: 0,
  },
  {
    id: 'e1',
    name: 'prov-a',
    base_url: 'https://a.example.com',
    api_key: 'sk-a***7890',
    protocol_anthropic: true,
    protocol_openai: false,
    auto_group: true,
    priority: 10,
    max_concurrency: 5,
    models: '["glm-4.6"]',
    enabled: true,
    builtin: false,
    health_status: 'ok',
    last_error: '',
    inflight: 1,
    egress_kernel: false,
    last_used_at: Math.floor(Date.now() / 1000) - 180,
    total_requests: 42,
    failed_requests: 2,
    cooldown_until: 0,
    rate_limited_until: 0,
  },
  // 非 anthropic 行:出口按钮不显示的负例(只有 O 协议)
  {
    id: 'e2',
    name: 'openai-only',
    base_url: 'https://o.example.com',
    api_key: 'sk-o***1234',
    protocol_anthropic: false,
    protocol_openai: true,
    auto_group: true,
    priority: 20,
    max_concurrency: 0,
    models: '[]',
    enabled: true,
    builtin: false,
    health_status: 'ok',
    last_error: '',
    inflight: 0,
    egress_kernel: false,
    last_used_at: 0,
    total_requests: 7,
    failed_requests: 1,
    cooldown_until: 0,
    rate_limited_until: 0,
  },
];

async function flush(rounds = 8) {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
}

function mountComp() {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(EndpointManagerSettings) });
  app.mount(el);
  return el;
}

function buttonByText(el, text) {
  return [...el.querySelectorAll('button')].find((btn) =>
    btn.textContent.replace(/\s+/g, ' ').trim().includes(text));
}

function setInput(el, field, value) {
  const input = el.querySelector(`[data-field="${field}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

function setChecked(el, field, value) {
  const input = el.querySelector(`[data-field="${field}"]`);
  input.checked = value;
  input.dispatchEvent(new Event('change'));
}

describe('EndpointManagerSettings LLM 端点管理卡', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(showErrorDialog).mockClear();
    getEndpoints.mockResolvedValue(endpointsData());
    getEndpointPolicy.mockResolvedValue({
      group: 'auto',
      failure_disable: true,
      failure_threshold: 3,
      cooldown_sec: 300,
      rate_limit_cooldown_sec: 60,
    });
    createEndpoint.mockResolvedValue({});
    updateEndpoint.mockResolvedValue({});
    deleteEndpoint.mockResolvedValue({ id: 'e1' });
    testEndpoint.mockResolvedValue({ ok: true, model_count: 12, error: '' });
    resetEndpointHealth.mockResolvedValue({ id: 'e1' });
    fetchEndpointModels.mockResolvedValue(['glm-4.6', 'glm-4.5']);
    setEndpointEgressKernel.mockResolvedValue({ id: 'e1', egress_kernel: true });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('挂载后渲染列表行:名称/协议徽标 A·O/builtin 徽标/掩码提示不出现完整 key', async () => {
    const el = mountComp();
    await flush();
    expect(el.textContent).toContain('prov-a');
    expect(el.textContent).toContain('Aliang 隧道（内置）');
    expect(el.textContent).toContain('endpointManager_builtinBadge');
    // 协议徽标 A/O(builtin 双开)
    const badges = [...el.querySelectorAll('span')].map((s) => s.textContent.trim());
    expect(badges).toContain('A');
    expect(badges).toContain('O');
  });

  it('§17 不变量 8:builtin 行删除按钮禁用且点击不发 DELETE', async () => {
    const el = mountComp();
    await flush();
    // 两行都有删除按钮;取 builtin 行内那颗(与重置健康按钮同容器序)
    const deleteButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.includes('endpointManager_delete')
        && !b.textContent.includes('endpointManager_deleteConfirm'));
    const builtinDelete = deleteButtons[0];
    expect(builtinDelete.disabled).toBe(true);
    builtinDelete.click();
    await flush();
    expect(deleteEndpoint).not.toHaveBeenCalled();
  });

  it('普通行删除:confirm 后发 DELETE', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const el = mountComp();
    await flush();
    const deleteButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_delete');
    expect(deleteButtons[1].disabled).toBe(false);
    deleteButtons[1].click();
    await flush();
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(deleteEndpoint).toHaveBeenCalledWith('e1');
    confirmSpy.mockRestore();
  });

  it('新增弹窗:双协议全 0 提交不发请求,报错走全局弹窗(与 Go Validate 同源)', async () => {
    const el = mountComp();
    await flush();
    buttonByText(el, 'endpointManager_add').click();
    await flush();
    setChecked(el, 'protocolAnthropic', false);
    setChecked(el, 'protocolOpenAI', false);
    setInput(el, 'name', 'x');
    buttonByText(el, 'endpointManager_save').click();
    await flush();
    expect(createEndpoint).not.toHaveBeenCalled();
    expect(showErrorDialog).toHaveBeenCalledWith('endpointManager_error_protocolRequired');
    expect(el.querySelector('[data-field="name"]')).toBeTruthy(); // 弹窗保持打开
  });

  it('新增成功:POST 载荷是线格式(models 为 JSON 数组串;enabled 不再进载荷,POST 缺键服务端默认 true)', async () => {
    const el = mountComp();
    await flush();
    buttonByText(el, 'endpointManager_add').click();
    await flush();
    setInput(el, 'name', 'fresh');
    setInput(el, 'baseUrl', 'https://gw.example.com');
    setInput(el, 'apiKey', 'sk-new');
    setInput(el, 'models', 'glm-4.6, claude-3');
    buttonByText(el, 'endpointManager_save').click();
    await flush();
    expect(createEndpoint).toHaveBeenCalledTimes(1);
    const payload = createEndpoint.mock.calls[0][0];
    expect(payload).toEqual({
      name: 'fresh',
      base_url: 'https://gw.example.com',
      api_key: 'sk-new',
      protocol_anthropic: true,
      protocol_openai: false,
      auto_group: true,
      priority: 0,
      max_concurrency: 0,
      models: '["glm-4.6","claude-3"]',
    });
    expect('enabled' in payload).toBe(false);
  });

  it('§17 不变量 7:编辑时 api_key 留空 → PUT 载荷不含 api_key 键(空=不改)', async () => {
    const el = mountComp();
    await flush();
    const editButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_edit');
    editButtons[1].click(); // prov-a 行
    await flush();
    setInput(el, 'name', 'prov-a-renamed');
    buttonByText(el, 'endpointManager_save').click();
    await flush();
    expect(updateEndpoint).toHaveBeenCalledWith('e1', expect.anything());
    const payload = updateEndpoint.mock.calls[0][1];
    expect(payload.name).toBe('prov-a-renamed');
    expect('api_key' in payload).toBe(false);
  });

  it('§17 不变量 8:编辑 builtin 行时 base_url/auto_group 输入禁用,载荷 base_url 恒空+auto_group 恒 true', async () => {
    const el = mountComp();
    await flush();
    const editButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_edit');
    editButtons[0].click(); // builtin 行
    await flush();
    expect(el.querySelector('[data-field="baseUrl"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="autoGroup"]').disabled).toBe(true);
    buttonByText(el, 'endpointManager_save').click();
    await flush();
    const payload = updateEndpoint.mock.calls[0][1];
    expect(payload.base_url).toBe('');
    expect(payload.auto_group).toBe(true);
  });

  it('final-review finding 6:builtin 行 priority 输入禁用,载荷 priority 兜底 0(builtin 恒第一,Store 层拒绝非 0)', async () => {
    const el = mountComp();
    await flush();
    const editButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_edit');
    editButtons[0].click(); // builtin 行
    await flush();
    // 编辑弹窗内 priority 输入禁用(builtin 行 priority 恒 0)
    expect(el.querySelector('[data-field="priority"]').disabled).toBe(true);
    buttonByText(el, 'endpointManager_save').click();
    await flush();
    const payload = updateEndpoint.mock.calls[0][1];
    // payload 侧兜底:即使表单残留非 0,线格式恒 0
    expect(payload.priority).toBe(0);
  });

  it('普通行 priority 输入可用(禁用只针对 builtin)', async () => {
    const el = mountComp();
    await flush();
    const editButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_edit');
    editButtons[1].click(); // prov-a 行
    await flush();
    expect(el.querySelector('[data-field="priority"]').disabled).toBe(false);
  });

  it('拉模型流程:POST fetch-models → 二级弹层展示 → 确认回填表单(不自动 PUT)→ 保存走 PUT', async () => {
    const el = mountComp();
    await flush();
    const fetchButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_fetchModels');
    fetchButtons[1].click(); // prov-a 行
    await flush();
    expect(fetchEndpointModels).toHaveBeenCalledWith('e1');
    // 二级弹层出现,列出模型
    expect(el.textContent).toContain('endpointManager_fetchedTitle');
    expect(el.textContent).toContain('glm-4.6');
    // 确认前不发 PUT
    expect(updateEndpoint).not.toHaveBeenCalled();
    const confirmBtn = buttonByText(el, 'endpointManager_fetchedConfirm');
    confirmBtn.click();
    await flush();
    // 弹层关闭,编辑表单打开且 models 文本域已回填,api_key 留空
    expect(el.querySelector('[data-field="models"]').value).toBe('glm-4.6\nglm-4.5');
    expect(el.querySelector('[data-field="apiKey"]').value).toBe('');
    expect(updateEndpoint).not.toHaveBeenCalled(); // 仍需用户点保存
    buttonByText(el, 'endpointManager_save').click();
    await flush();
    expect(updateEndpoint).toHaveBeenCalledTimes(1);
    const payload = updateEndpoint.mock.calls[0][1];
    expect(payload.models).toBe('["glm-4.6","glm-4.5"]');
  });

  it('测试连接:结果写卡片通知条(ok 绿/失败琥珀),失败不弹全局错误框', async () => {
    const el = mountComp();
    await flush();
    const testButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_test');
    testButtons[1].click();
    await flush();
    expect(testEndpoint).toHaveBeenCalledWith('e1');
    expect(el.textContent).toContain('endpointManager_testOk');
    // 失败形态
    testEndpoint.mockResolvedValue({ ok: false, model_count: 0, error: 'dial timeout' });
    testButtons[1].click();
    await flush();
    expect(el.textContent).toContain('endpointManager_testFailed');
    expect(showErrorDialog).not.toHaveBeenCalled();
  });

  it('重置健康:POST reset-health 后刷新列表', async () => {
    const el = mountComp();
    await flush();
    const resetButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_resetHealth');
    resetButtons[1].click();
    await flush();
    expect(resetEndpointHealth).toHaveBeenCalledWith('e1');
    expect(getEndpoints.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(el.textContent).toContain('endpointManager_resetHealthDone');
  });

  // —— 二期:行上激活开关 / 出口指定 / 三词徽标 / 行尾计数(spec 二期 §7.1)——

  function rowToggle(el, index) {
    return [...el.querySelectorAll('[data-field="rowEnabledToggle"]')][index];
  }

  it('行上激活开关:点击 = PUT enabled 快速路径(不进弹窗)并刷新列表', async () => {
    const el = mountComp();
    await flush();
    expect(getEndpoints.mock.calls.length).toBe(1);
    // 第二行开关 = prov-a(e1),当前启用 → 停用
    rowToggle(el, 1).click();
    await flush();
    expect(updateEndpoint).toHaveBeenCalledTimes(1);
    expect(updateEndpoint).toHaveBeenCalledWith('e1', { enabled: false });
    expect(getEndpoints.mock.calls.length).toBeGreaterThanOrEqual(2); // 刷新
    // 编辑弹窗没有因此打开
    expect(el.querySelector('[data-field="name"]')).toBeNull();
  });

  it('行上激活开关:停用行点击 → PUT {enabled:true} 重新启用', async () => {
    getEndpoints.mockResolvedValue(endpointsData().map((r) => (
      r.id === 'e1' ? { ...r, enabled: false } : r)));
    const el = mountComp();
    await flush();
    rowToggle(el, 1).click();
    await flush();
    expect(updateEndpoint).toHaveBeenCalledWith('e1', { enabled: true });
  });

  it('停用行视觉区分:整行灰显 + 健康徽标位显示「已停用」(灰),不再另挂第二枚停用徽标', async () => {
    getEndpoints.mockResolvedValue(endpointsData().map((r) => (
      r.id === 'e1' ? { ...r, enabled: false } : r)));
    const el = mountComp();
    await flush();
    const items = [...el.querySelectorAll('ul > li')];
    expect(items[1].className).toContain('opacity-60');
    const badges = [...items[1].querySelectorAll('[data-testid="endpoint-health-badge"]')]
      .map((s) => s.textContent.trim());
    expect(badges).toEqual(['endpointManager_disabledBadge']);
    const text = items[1].textContent;
    expect(text.split('endpointManager_disabledBadge').length - 1).toBe(1); // 恰好一枚
    // 开关 aria 态同步
    expect(rowToggle(el, 1).getAttribute('aria-checked')).toBe('false');
  });

  it('出口按钮:anthropic 行显示「设为 AliangCode 出口」(builtin 可设),非 anthropic 行不显示', async () => {
    const el = mountComp();
    await flush();
    const setBtns = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_setEgress');
    expect(setBtns.length).toBe(2); // builtin + prov-a(双 anthropic);openai-only 无
    const items = [...el.querySelectorAll('ul > li')];
    expect(items[2].textContent).not.toContain('endpointManager_setEgress');
    expect(items[2].textContent).not.toContain('endpointManager_unsetEgress');
  });

  it('出口换绑:点击设出口 → POST set-egress-kernel {set:true} + 刷新 + 行内成功提示', async () => {
    const el = mountComp();
    await flush();
    const setBtns = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_setEgress');
    setBtns[1].click(); // prov-a
    await flush();
    expect(setEndpointEgressKernel).toHaveBeenCalledTimes(1);
    expect(setEndpointEgressKernel).toHaveBeenCalledWith('e1', true);
    expect(getEndpoints.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(el.textContent).toContain('endpointManager_egressSet');
  });

  it('当前出口行:徽标「出口」(primary) + 按钮变「取消出口」,取消 → POST {set:false}', async () => {
    getEndpoints.mockResolvedValue(endpointsData().map((r) => (
      r.id === 'e1' ? { ...r, egress_kernel: true } : r)));
    const el = mountComp();
    await flush();
    // 徽标只在出口行
    const badgeRows = [...el.querySelectorAll('[data-testid="endpoint-egress-badge"]')];
    expect(badgeRows.length).toBe(1);
    expect(badgeRows[0].textContent.trim()).toBe('endpointManager_egressBadge');
    expect(badgeRows[0].className).toContain('bg-primary');
    // 当前出口行按钮 = 取消出口
    const unsetBtns = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_unsetEgress');
    expect(unsetBtns.length).toBe(1);
    unsetBtns[0].click();
    await flush();
    expect(setEndpointEgressKernel).toHaveBeenCalledWith('e1', false);
    expect(el.textContent).toContain('endpointManager_egressUnset');
  });

  it('三词徽章·熔断红:disabled+冷却中行 tooltip「恢复于 HH:MM」;限流行 tooltip「限流至 HH:MM」', async () => {
    const recoverAt = Math.floor(Date.now() / 1000) + 600;
    const limitedUntil = Math.floor(Date.now() / 1000) + 300;
    getEndpoints.mockResolvedValue(endpointsData().map((r) => {
      if (r.id === 'e1') return { ...r, health_status: 'disabled', cooldown_until: recoverAt };
      if (r.id === 'e2') return { ...r, rate_limited_until: limitedUntil };
      return r;
    }));
    const el = mountComp();
    await flush();
    const items = [...el.querySelectorAll('ul > li')];
    const e1Badge = items[1].querySelector('[data-testid="endpoint-health-badge"]');
    expect(e1Badge.textContent.trim()).toBe('endpointManager_healthDisabled');
    expect(e1Badge.getAttribute('title'))
      .toBe(`endpointManager_tooltipRecoverAt ${formatClockTime(recoverAt)}`);
    const e2Badge = items[2].querySelector('[data-testid="endpoint-health-badge"]');
    expect(e2Badge.textContent.trim()).toBe('endpointManager_healthRateLimited');
    expect(e2Badge.getAttribute('title'))
      .toBe(`endpointManager_tooltipLimitedUntil ${formatClockTime(limitedUntil)}`);
    // builtin 行无 tooltip
    expect(items[0].querySelector('[data-testid="endpoint-health-badge"]').getAttribute('title')).toBe('');
  });

  it('行尾计数:调用/失败 + last_used_at 相对时间(3 分钟前);从未调用行显示从未', async () => {
    const el = mountComp();
    await flush();
    const tails = [...el.querySelectorAll('[data-testid="endpoint-usage-tail"]')]
      .map((s) => s.textContent.replace(/\s+/g, ' ').trim());
    expect(tails[1]).toContain('endpointManager_totalRequests 42');
    expect(tails[1]).toContain('endpointManager_failedRequests 2');
    expect(tails[1]).toContain('endpointManager_lastUsedMinAgo 3');
    // builtin 与 openai-only 的 last_used_at=0 → 从未调用
    expect(tails[0]).toContain('endpointManager_lastUsedNever');
    expect(tails[2]).toContain('endpointManager_lastUsedNever');
  });

  it('二期:编辑弹窗不再有 enabled 勾选(行上开关取代)', async () => {
    const el = mountComp();
    await flush();
    const editButtons = [...el.querySelectorAll('button')]
      .filter((b) => b.textContent.replace(/\s+/g, ' ').trim() === 'endpointManager_edit');
    editButtons[1].click();
    await flush();
    expect(el.querySelector('[data-field="enabled"]')).toBeNull();
  });

  it('组策略卡:挂载读取默认值,保存走 PUT(数值为整数)', async () => {
    const el = mountComp();
    await flush();
    expect(el.querySelector('[data-field="policyThreshold"]').value).toBe('3');
    expect(el.querySelector('[data-field="policyFailureDisable"]').checked).toBe(true);
    buttonByText(el, 'endpointManager_policySave').click();
    await flush();
    expect(setEndpointPolicy).toHaveBeenCalledWith({
      failure_disable: true,
      failure_threshold: 3,
      cooldown_sec: 300,
      rate_limit_cooldown_sec: 60,
    });
  });

  it('组策略负数拒:不发 PUT 且行内报错(与 SetPolicy ≥0 同源)', async () => {
    const el = mountComp();
    await flush();
    setInput(el, 'policyThreshold', '-1');
    buttonByText(el, 'endpointManager_policySave').click();
    await flush();
    expect(setEndpointPolicy).not.toHaveBeenCalled();
    expect(el.textContent).toContain('endpointManager_error_policyValues');
  });

  it('组策略空串拒:空输入不静默写 0(quality-fix #4,阈值 0=首败即摘除)', async () => {
    const el = mountComp();
    await flush();
    setInput(el, 'policyThreshold', '');
    buttonByText(el, 'endpointManager_policySave').click();
    await flush();
    expect(setEndpointPolicy).not.toHaveBeenCalled();
    expect(el.textContent).toContain('endpointManager_error_policyValues');
    // 恢复合法值后可正常保存(错误条撤离)
    setInput(el, 'policyThreshold', '3');
    buttonByText(el, 'endpointManager_policySave').click();
    await flush();
    expect(setEndpointPolicy).toHaveBeenCalledTimes(1);
    expect(setEndpointPolicy.mock.calls[0][0].failure_threshold).toBe(3);
    expect(el.textContent).not.toContain('endpointManager_error_policyValues');
  });
});

describe('endpointHealthBadge / endpointToneClass 三词徽标纯函数(不变量 16)', () => {
  // 固定「现在」,cooldown/ratelimited 窗口以它为基准
  const NOW = 1700000000000;
  const future = NOW / 1000 + 600; // +10 分钟(unix 秒)

  it('停用最高优先:enabled=0 → 灰(已停用),健康列不再展示', () => {
    expect(endpointHealthBadge({ enabled: false, healthStatus: 'ok' }, NOW)).toEqual({
      key: 'endpointManager_disabledBadge', tone: 'muted',
    });
    // 停用行即使挂着熔断/限流态,徽标仍只说「已停用」——不混词
    expect(endpointHealthBadge({
      enabled: false, healthStatus: 'disabled', cooldownUntil: future,
    }, NOW).key).toBe('endpointManager_disabledBadge');
  });

  it('熔断红:health=disabled ∧ cooldown_until>now → tooltip 恢复于 HH:MM', () => {
    const badge = endpointHealthBadge({
      enabled: true, healthStatus: 'disabled', cooldownUntil: future,
    }, NOW);
    expect(badge.tone).toBe('error');
    expect(badge.key).toBe('endpointManager_healthDisabled');
    expect(badge.tooltipKey).toBe('endpointManager_tooltipRecoverAt');
    expect(badge.tooltipTime).toBe(formatClockTime(future));
    expect(badge.tooltipTime).toMatch(/^\d{2}:\d{2}$/);
  });

  it('熔断红·锁存:health=disabled ∧ cooldown_until=0(401/402 不自愈)→ 无 tooltip', () => {
    const badge = endpointHealthBadge({
      enabled: true, healthStatus: 'disabled', cooldownUntil: 0,
    }, NOW);
    expect(badge.tone).toBe('error');
    expect(badge.tooltipKey).toBeUndefined();
    expect(badge.tooltipTime).toBeUndefined();
  });

  it('熔断红·冷却已过:cooldown_until<=now → 红但无 tooltip(等真实成功/重置回池)', () => {
    const badge = endpointHealthBadge({
      enabled: true, healthStatus: 'disabled', cooldownUntil: future - 1200,
    }, NOW);
    expect(badge.tone).toBe('error');
    expect(badge.tooltipKey).toBeUndefined();
  });

  it('限流蓝灰:health 仍 ok ∧ rate_limited_until>now → tooltip 限流至 HH:MM', () => {
    const badge = endpointHealthBadge({
      enabled: true, healthStatus: 'ok', rateLimitedUntil: future,
    }, NOW);
    expect(badge).toEqual({
      key: 'endpointManager_healthRateLimited',
      tone: 'muted',
      tooltipKey: 'endpointManager_tooltipLimitedUntil',
      tooltipTime: formatClockTime(future),
    });
  });

  it('限流窗已过(rate_limited_until<=now)回正常——不再看 last_error 的陈旧 429 文案', () => {
    expect(endpointHealthBadge({
      enabled: true, healthStatus: 'ok', rateLimitedUntil: future - 1200,
      lastError: 'HTTP 429: rate limited',
    }, NOW)).toEqual({ key: 'endpointManager_healthOk', tone: 'ok' });
    expect(endpointHealthBadge({
      enabled: true, healthStatus: 'ok', rateLimitedUntil: 0,
    }, NOW).key).toBe('endpointManager_healthOk');
  });

  it('熔断压过限流:disabled ∧ rate_limited_until>now → 红熔断(严重度优先)', () => {
    const badge = endpointHealthBadge({
      enabled: true, healthStatus: 'disabled', cooldownUntil: 0, rateLimitedUntil: future,
    }, NOW);
    expect(badge.key).toBe('endpointManager_healthDisabled');
    expect(badge.tone).toBe('error');
  });

  it('legacy cooldown 健康值(一期历史行)按非 ok → 熔断红', () => {
    expect(endpointHealthBadge({
      enabled: true, healthStatus: 'cooldown', cooldownUntil: future,
    }, NOW).tone).toBe('error');
  });

  it('正常绿 + 缺省回落/未知 tone 回落 muted', () => {
    expect(endpointHealthBadge({ enabled: true, healthStatus: 'ok' }, NOW)).toEqual({
      key: 'endpointManager_healthOk', tone: 'ok',
    });
    expect(endpointHealthBadge({}, NOW).key).toBe('endpointManager_healthOk');
    expect(endpointHealthBadge(null, NOW).tone).toBe('ok');
    expect(endpointToneClass('ok')).toContain('bg-emerald-100');
    expect(endpointToneClass('warn')).toContain('bg-amber-100');
    expect(endpointToneClass('error')).toContain('bg-red-100');
    expect(endpointToneClass('muted')).toContain('bg-slate-100');
    expect(endpointToneClass('bogus')).toBe(endpointToneClass('muted'));
  });
});
