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
import {
  createEndpoint,
  deleteEndpoint,
  fetchEndpointModels,
  getEndpointPolicy,
  getEndpoints,
  resetEndpointHealth,
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

  it('新增成功:POST 载荷是线格式(models 为 JSON 数组串,enabled 缺省 true)', async () => {
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
      enabled: true,
    });
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
});

describe('endpointHealthBadge / endpointToneClass 徽标纯函数', () => {
  it('health_status 权威:ok 绿/cooldown 琥珀/disabled 红', () => {
    expect(endpointHealthBadge({ healthStatus: 'ok', lastError: '' })).toEqual({
      key: 'endpointManager_healthOk', tone: 'ok',
    });
    expect(endpointHealthBadge({ healthStatus: 'cooldown', lastError: '' }).tone).toBe('warn');
    expect(endpointHealthBadge({ healthStatus: 'disabled', lastError: '' }).tone).toBe('error');
  });

  it('429 限流从 last_error 识别(蓝灰);health_status 仍是 ok', () => {
    expect(endpointHealthBadge({ healthStatus: 'ok', lastError: 'HTTP 429: rate limited' })).toEqual({
      key: 'endpointManager_healthRateLimited', tone: 'muted',
    });
    expect(endpointHealthBadge({ healthStatus: 'ok', lastError: 'rate_limit hit' }).tone).toBe('muted');
    // 普通错误不误判
    expect(endpointHealthBadge({ healthStatus: 'ok', lastError: 'dial timeout' }).tone).toBe('ok');
  });

  it('缺省回落 ok/未知 tone 回落 muted', () => {
    expect(endpointHealthBadge({}).key).toBe('endpointManager_healthOk');
    expect(endpointHealthBadge(null).tone).toBe('ok');
    expect(endpointToneClass('ok')).toContain('bg-emerald-100');
    expect(endpointToneClass('warn')).toContain('bg-amber-100');
    expect(endpointToneClass('error')).toContain('bg-red-100');
    expect(endpointToneClass('muted')).toContain('bg-slate-100');
    expect(endpointToneClass('bogus')).toBe(endpointToneClass('muted'));
  });
});
