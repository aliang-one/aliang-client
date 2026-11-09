// 记忆管理卡（记忆批次 Task 7）。挂载用 createApp + jsdom 手法（仓内无
// @vue/test-utils，AgentMCPRegistrySettings.test.js 同范式）。组件用裸 fetch
// 直连 /api/agent/memory*（信封解法与 services/agentApi.request 同构），
// 故 mock global.fetch——既有三条 mock 依赖（i18n 键回吐/agentApi）中
// agentApi 本卡不用，不需要 mock。
// 断言走 i18n 键名（mock t 原样回键），不硬编码文案。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../i18n', () => ({
  // 键原样回吐，参数值尾随拼接：让 *_confirm/*_count 这类插值文案的参数
  // （如项目名/条数）也能被断言到。
  useI18n: () => ({ t: (k, params = {}) => (params && Object.keys(params).length ? `${k} ${Object.values(params).join(' ')}` : k) }),
}));

import { createApp, h } from 'vue';
import AgentMemorySettings, {
  MEMORY_CONTENT_CAP,
  capMemoryContent,
  formatMemoryTime,
  memoryChoiceFromConfig,
} from './AgentMemorySettings.vue';

const projectsData = () => [{ project: 'alpha', count: 2 }];
const memoriesData = () => [
  { id: 1, content: '记得部署脚本', created_at: '2026-11-09T01:02:03Z', source: 'extract' },
  { id: 2, content: '手动记一条', created_at: '2026-11-08T00:00:00Z', source: 'manual' },
];

let fetchMock;

function jsonResponse(data) {
  return { ok: true, json: async () => ({ code: 0, msg: '', data }) };
}

// 按路径分派：projects / 列表（project 查询参数在首条之后出现）/ 写端点默认空 data。
function mockMemoryEndpoints() {
  fetchMock.mockImplementation(async (path) => {
    const url = String(path);
    if (url === '/api/agent/memory/projects') return jsonResponse({ projects: projectsData() });
    if (url.startsWith('/api/agent/memory?project=')) return jsonResponse({ memories: memoriesData() });
    return jsonResponse({});
  });
}

// 微任务冲刷 + 一个宏任务兜底：mounted → loadAll → loadMemories 的 await 链
// 加上 finally 里 working=false 的尾批微任务可能超过纯微任务轮次；setTimeout(0)
// 只在微任务队列清空后运行，保证断言前整链（含行按钮 disabled 复位）已落定。
// 否则会出现「点击落在 pending patch 窗口被 jsdom 静默吞掉」的假失败。
async function flush(rounds = 12) {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function mountComp(config = {}) {
  const el = document.createElement('div');
  const onSave = vi.fn();
  const app = createApp({
    render: () => h(AgentMemorySettings, { config, onSave }),
  });
  app.mount(el);
  return { el, onSave };
}

// 各按钮文案键互不为子串（同族 registry 卡先例），归一空白后按包含匹配。
function buttonByText(el, text) {
  return [...el.querySelectorAll('button')].find((btn) =>
    btn.textContent.replace(/\s+/g, ' ').trim().includes(text));
}

function setSelect(el, testid, value) {
  const select = el.querySelector(`[data-testid="${testid}"]`);
  select.value = value;
  select.dispatchEvent(new Event('change'));
}

function setInput(el, field, value) {
  const input = el.querySelector(`[data-field="${field}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

describe('AgentMemorySettings 记忆管理卡', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    mockMemoryEndpoints();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('挂载后渲染项目下拉、条目内容/时间/来源徽标', async () => {
    const { el } = mountComp();
    await flush();
    // 项目下拉取首项，条数徽标走 agent_memory_count 插值
    expect(el.querySelector('[data-testid="memory-project"]').value).toBe('alpha');
    expect(el.textContent).toContain('agent_memory_count 2');
    // 两条目内容 + 来源徽标（提取/手动两态）
    expect(el.textContent).toContain('记得部署脚本');
    expect(el.textContent).toContain('手动记一条');
    expect(el.textContent).toContain('agent_memory_source_extract');
    expect(el.textContent).toContain('agent_memory_source_manual');
    // 时间列 = created_at 的本地化格式（测试侧同款计算保证跨 ICU 一致）
    expect(el.textContent).toContain(formatMemoryTime('2026-11-09T01:02:03Z'));
    // 首拉序列：先 projects 后列表
    expect(fetchMock.mock.calls[0][0]).toBe('/api/agent/memory/projects');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/agent/memory?project=alpha');
  });

  it('开关三态初值与 emit save payload（含「跟随默认」缺键）', async () => {
    // 初值 off → 切 on：payload 显式 true，原 agentcore 其余键随段透传
    const off = mountComp({ agentcore: { memory_enabled: false, max_identical_tool_turns: 3 } });
    await flush();
    expect(off.el.querySelector('[data-testid="memory-switch"]').value).toBe('off');
    setSelect(off.el, 'memory-switch', 'on');
    await flush();
    const payloadOn = off.onSave.mock.calls.at(-1)[0];
    expect(payloadOn.agentcore.memory_enabled).toBe(true);
    expect(payloadOn.agentcore.max_identical_tool_turns).toBe(3);

    // 初值 on → 切 follow：显式剔除键（缺键不进 patch，归一层三态契约）
    const on = mountComp({ agentcore: { memory_enabled: true } });
    await flush();
    expect(on.el.querySelector('[data-testid="memory-switch"]').value).toBe('on');
    setSelect(on.el, 'memory-switch', 'follow');
    await flush();
    const payloadFollow = on.onSave.mock.calls.at(-1)[0];
    expect('memory_enabled' in payloadFollow.agentcore).toBe(false);

    // 未配置（nil）初值 = follow → 切 off：payload 显式 false
    const nil = mountComp({});
    await flush();
    expect(nil.el.querySelector('[data-testid="memory-switch"]').value).toBe('follow');
    setSelect(nil.el, 'memory-switch', 'off');
    await flush();
    expect(nil.onSave.mock.calls.at(-1)[0].agentcore.memory_enabled).toBe(false);
  });

  it('删除走确认：拒绝不发请求，确认后 DELETE 单条并刷新', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const { el } = mountComp();
    await flush();
    buttonByText(el, 'agent_memory_delete').click();
    await flush();
    // 确认弹层出现且文案携带项目名
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy.mock.calls[0][0]).toContain('alpha');
    // 拒绝：无 DELETE，无重拉
    const writes = fetchMock.mock.calls.filter((call) => call[1] && call[1].method === 'DELETE');
    expect(writes).toHaveLength(0);

    confirmSpy.mockReturnValue(true);
    buttonByText(el, 'agent_memory_delete').click();
    await flush();
    const deleteCalls = fetchMock.mock.calls.filter((call) => call[1] && call[1].method === 'DELETE');
    expect(deleteCalls).toHaveLength(1);
    expect(deleteCalls[0][0]).toBe('/api/agent/memory/1?project=alpha');
    // 成功后重拉（projects + 列表 ≥ 各 2 次）
    expect(fetchMock.mock.calls.filter((call) => call[0] === '/api/agent/memory/projects').length).toBeGreaterThanOrEqual(2);
  });

  it('删除 404 信封（条目已被并发删除）静默刷新；非 JSON 404（漏注册）显性报错', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { el } = mountComp();
    await flush();
    const projectCalls = () => fetchMock.mock.calls.filter((call) => call[0] === '/api/agent/memory/projects').length;
    // 场景 A：404 业务信封（common.ErrorNotFound 形态）——双写者下删陈旧行
    // 竞态，按良性处理：不报错、照常重拉。
    fetchMock.mockImplementation(async (path, options) => {
      if (options && options.method === 'DELETE') {
        return { ok: false, status: 404, json: async () => ({ code: 404, msg: '记忆条目不存在' }) };
      }
      if (String(path) === '/api/agent/memory/projects') return jsonResponse({ projects: projectsData() });
      if (String(path).startsWith('/api/agent/memory?project=')) return jsonResponse({ memories: memoriesData() });
      return jsonResponse({});
    });
    const before404 = projectCalls();
    buttonByText(el, 'agent_memory_delete').click();
    await flush();
    expect(el.textContent).not.toContain('agent_memory_action_failed');
    expect(projectCalls()).toBeGreaterThan(before404);

    // 场景 B：非 JSON 体 404（路由漏注册返回 404 page not found 的配置事故）
    // ——无信封即无 status，isMissingError 判否，错误横幅显性出现。
    fetchMock.mockImplementation(async (path, options) => {
      if (options && options.method === 'DELETE') {
        return {
          ok: false,
          status: 404,
          json: async () => { throw new TypeError('Unexpected token < in JSON at position 0'); },
        };
      }
      if (String(path) === '/api/agent/memory/projects') return jsonResponse({ projects: projectsData() });
      if (String(path).startsWith('/api/agent/memory?project=')) return jsonResponse({ memories: memoriesData() });
      return jsonResponse({});
    });
    buttonByText(el, 'agent_memory_delete').click();
    await flush();
    expect(el.textContent).toContain('agent_memory_action_failed');
    expect(el.textContent).toContain('Request failed with HTTP 404');
  });

  it('写通道业务失败信封（code=1）出现错误横幅且弹窗保持打开', async () => {
    const { el } = mountComp();
    await flush();
    fetchMock.mockImplementation(async (path) => {
      if (String(path) === '/api/agent/memory') {
        return { ok: true, json: async () => ({ code: 1, msg: '存储器坏了' }) };
      }
      if (String(path) === '/api/agent/memory/projects') return jsonResponse({ projects: projectsData() });
      if (String(path).startsWith('/api/agent/memory?project=')) return jsonResponse({ memories: memoriesData() });
      return jsonResponse({});
    });
    buttonByText(el, 'agent_memory_add').click();
    await flush();
    setInput(el, 'memory-content', '会失败的一条');
    buttonByText(el, 'agent_memory_save').click();
    await flush();
    // 横幅 = 保存失败文案 + 服务端 msg 原文（i18n mock 尾随拼参数）
    expect(el.textContent).toContain('agent_memory_save_failed 存储器坏了');
    // 弹窗保持打开，等用户修正后重试
    expect(el.querySelector('[data-field="memory-content"]')).toBeTruthy();
  });

  it('500 字符上限：程序化赋值截断、计数到顶、空内容不发请求', async () => {
    const { el } = mountComp();
    await flush();
    buttonByText(el, 'agent_memory_add').click();
    await flush();
    const textarea = el.querySelector('[data-field="memory-content"]');
    expect(textarea).toBeTruthy();
    // 501 字符程序化赋值（jsdom 绕过 maxlength）→ input 处理器截到 500
    setInput(el, 'memory-content', '记'.repeat(MEMORY_CONTENT_CAP + 1));
    await flush();
    expect(el.querySelector('[data-field="memory-content"]').value.length).toBe(MEMORY_CONTENT_CAP);
    expect(el.querySelector('[data-testid="memory-content-counter"]').textContent.replace(/\s+/g, ' '))
      .toContain(`${MEMORY_CONTENT_CAP} / ${MEMORY_CONTENT_CAP}`);

    // 清空后保存：客户端校验不发请求
    setInput(el, 'memory-content', '   ');
    buttonByText(el, 'agent_memory_save').click();
    await flush();
    const posts = fetchMock.mock.calls.filter((call) => call[1] && call[1].method === 'POST');
    expect(posts).toHaveLength(0);
    expect(el.textContent).toContain('agent_memory_content_required');
    // 弹窗保持打开
    expect(el.querySelector('[data-field="memory-content"]')).toBeTruthy();

    // 合法内容：POST 载荷 = {project, content}
    setInput(el, 'memory-content', '新的记忆条目');
    buttonByText(el, 'agent_memory_save').click();
    await flush();
    const postCalls = fetchMock.mock.calls.filter((call) => call[1] && call[1].method === 'POST');
    expect(postCalls).toHaveLength(1);
    expect(postCalls[0][0]).toBe('/api/agent/memory');
    expect(JSON.parse(postCalls[0][1].body)).toEqual({ project: 'alpha', content: '新的记忆条目' });
  });

  it('清空必须确认：确认后 DELETE 整项目（无 id 路径）并刷新', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { el } = mountComp();
    await flush();
    buttonByText(el, 'agent_memory_clear').click();
    await flush();
    // 确认文案带项目名与条数
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy.mock.calls[0][0]).toContain('alpha');
    expect(confirmSpy.mock.calls[0][0]).toContain('2');
    const deleteCalls = fetchMock.mock.calls.filter((call) => call[1] && call[1].method === 'DELETE');
    expect(deleteCalls).toHaveLength(1);
    expect(deleteCalls[0][0]).toBe('/api/agent/memory?project=alpha');
  });

  it('行内编辑回填内容，保存发 PUT 并刷新', async () => {
    const { el } = mountComp();
    await flush();
    buttonByText(el, 'agent_memory_edit').click();
    await flush();
    const editArea = el.querySelector('[data-field="memory-edit-content"]');
    expect(editArea).toBeTruthy();
    expect(editArea.value).toBe('记得部署脚本');
    setInput(el, 'memory-edit-content', '更新后的记忆');
    buttonByText(el, 'agent_memory_save').click();
    await flush();
    const putCalls = fetchMock.mock.calls.filter((call) => call[1] && call[1].method === 'PUT');
    expect(putCalls).toHaveLength(1);
    expect(putCalls[0][0]).toBe('/api/agent/memory/1');
    expect(JSON.parse(putCalls[0][1].body)).toEqual({ project: 'alpha', content: '更新后的记忆' });
  });
});

// 导出纯函数直测（registry 卡 parseKeyValueLines 先例）。
describe('memoryChoiceFromConfig / capMemoryContent / formatMemoryTime', () => {
  it('三态映射：显式布尔才映射，其余一律 follow', () => {
    expect(memoryChoiceFromConfig(true)).toBe('on');
    expect(memoryChoiceFromConfig(false)).toBe('off');
    expect(memoryChoiceFromConfig(undefined)).toBe('follow');
    expect(memoryChoiceFromConfig(null)).toBe('follow');
    expect(memoryChoiceFromConfig('true')).toBe('follow');
    expect(memoryChoiceFromConfig(1)).toBe('follow');
  });

  it('500 上限截断，短内容原样，空安全', () => {
    expect(capMemoryContent('a'.repeat(501)).length).toBe(500);
    expect(capMemoryContent('短内容')).toBe('短内容');
    expect(capMemoryContent(undefined)).toBe('');
    expect(capMemoryContent(null)).toBe('');
    expect(MEMORY_CONTENT_CAP).toBe(500);
  });

  it('时间：合法 RFC3339 本地化，畸形原样透出', () => {
    expect(formatMemoryTime('2026-11-09T01:02:03Z')).toBe(new Date('2026-11-09T01:02:03Z').toLocaleString());
    expect(formatMemoryTime('not-a-date')).toBe('not-a-date');
    expect(formatMemoryTime('')).toBe('');
  });
});
