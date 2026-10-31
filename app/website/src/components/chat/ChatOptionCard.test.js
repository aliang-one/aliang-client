// Task 14：ai.option 两步作答交互卡组件（vitest）。仓内无 @vue/test-utils，
// 沿用 ChatMarkdown.test.js 的 createApp + document.body 挂载手法。卡对象用
// reactive() 包裹后再作 prop：真实链路里 optionCards 挂在 conversations ref
// 深层，useAliangChat.respondOption 原位改 status/selected；裸对象 prop 的
// 深层变更不触发重渲染，必须还原响应性再断言终态。
import { describe, it, expect } from 'vitest';
import { createApp, h, nextTick, reactive } from 'vue';
import ChatOptionCard from './ChatOptionCard.vue';
import { useI18n } from '../../i18n';

const { t } = useI18n();

function makeCard(overrides = {}) {
  return {
    key: 's1:op1',
    optionId: 'op1',
    sessionId: 's1',
    messageId: '',
    provider: 'agentcore',
    title: '部署方式',
    header: '',
    summary: '请选择',
    options: [
      { id: 'a', label: '方案 A', description: '蓝绿', response: '', previews: [] },
      { id: 'b', label: '方案 B', description: '直接替换', response: '', previews: [] },
    ],
    allowCustom: false,
    multi: false,
    status: 'pending',
    selected: [],
    customText: '',
    error: '',
    sourceId: '',
    updatedAt: 1,
    ...overrides,
  };
}

function mountCard(card) {
  const el = document.createElement('div');
  const events = [];
  const app = createApp({
    render: () => h(ChatOptionCard, {
      card,
      onRespond: (payload) => events.push(payload),
    }),
  });
  app.mount(el);
  return { el, events, app };
}

async function mountVisible(card) {
  const mounted = mountCard(card);
  await nextTick();
  return mounted;
}

describe('ChatOptionCard', () => {
  it('单选互斥：点 A 再点 B，仅 B 选中', async () => {
    const m = await mountVisible(makeCard());
    const rows = () => m.el.querySelectorAll('[role="radio"]');
    rows()[0].click();
    await nextTick();
    expect(rows()[0].getAttribute('aria-checked')).toBe('true');
    expect(rows()[1].getAttribute('aria-checked')).toBe('false');
    rows()[1].click();
    await nextTick();
    expect(rows()[0].getAttribute('aria-checked')).toBe('false');
    expect(rows()[1].getAttribute('aria-checked')).toBe('true');
  });

  it('多选可叠加：multi 卡点 A 与 B，两项同时选中', async () => {
    const m = await mountVisible(makeCard({ multi: true }));
    const rows = () => m.el.querySelectorAll('[role="checkbox"]');
    rows()[0].click();
    rows()[1].click();
    await nextTick();
    expect(rows()[0].getAttribute('aria-checked')).toBe('true');
    expect(rows()[1].getAttribute('aria-checked')).toBe('true');
  });

  it('无选中时提交禁用，选中后解除', async () => {
    const m = await mountVisible(makeCard());
    const submit = () => m.el.querySelector('[data-testid="chat-option-submit"]');
    expect(submit().disabled).toBe(true);
    m.el.querySelector('[role="radio"]').click();
    await nextTick();
    expect(submit().disabled).toBe(false);
  });

  it('allow_custom：自定义文本随 respond 事件进 custom_text', async () => {
    const m = await mountVisible(makeCard({ allowCustom: true }));
    m.el.querySelector('[role="radio"]').click();
    const ta = m.el.querySelector('textarea');
    ta.value = '补充灰度';
    ta.dispatchEvent(new Event('input'));
    await nextTick();
    m.el.querySelector('[data-testid="chat-option-submit"]').click();
    expect(m.events).toEqual([{ selected: ['a'], customText: '补充灰度' }]);
  });

  it('提交后折叠终态：已选择 + 选项行退场（status 由上层 respondOption 置位）', async () => {
    const card = reactive(makeCard());
    const m = await mountVisible(card);
    m.el.querySelector('[role="radio"]').click();
    await nextTick(); // 等 disabled 解除落到 DOM（jsdom 的 disabled 按钮不派发 click）
    m.el.querySelector('[data-testid="chat-option-submit"]').click();
    expect(m.events.length).toBe(1);
    // 上层发送成功后置 answered + 记录选择（组件只发事件，终态由状态驱动）
    card.status = 'answered';
    card.selected = ['a'];
    await nextTick();
    expect(m.el.textContent).toContain(t('chat_optionSelected'));
    expect(m.el.textContent).toContain('方案 A');
    expect(m.el.querySelectorAll('[role="radio"]').length).toBe(0);
  });

  it('error 显示错误文案且可重新选择提交（恢复可选）', async () => {
    const card = reactive(makeCard({ error: t('chat_optionSendFailed') }));
    const m = await mountVisible(card);
    expect(m.el.textContent).toContain(t('chat_optionSendFailed'));
    m.el.querySelector('[role="radio"]').click();
    await nextTick();
    m.el.querySelector('[data-testid="chat-option-submit"]').click();
    expect(m.events).toEqual([{ selected: ['a'], customText: '' }]);
  });

  it('previews 折叠块是选项行兄弟而非 button 后代，开合预览不翻转选中态', async () => {
    const card = reactive(makeCard({
      options: [
        { id: 'a', label: '方案 A', description: '', response: '', previews: ['api: GET /v1/deploy'] },
      ],
    }));
    const m = await mountVisible(card);
    const details = m.el.querySelector('details');
    expect(details).toBeTruthy();
    expect(details.textContent).toContain(t('chat_optionPreview'));
    // HTML 内容模型：button 禁交互后代，details 必须在按钮外
    expect(m.el.querySelector('button details')).toBeNull();
    // 开合预览（summary 点击）不顺带翻转选项选中态
    m.el.querySelector('[role="radio"]').click();
    await nextTick();
    details.querySelector('summary').click();
    await nextTick();
    expect(m.el.querySelector('[role="radio"]').getAttribute('aria-checked')).toBe('true');
  });

  it('answeredSummary 分隔符 locale 中立（逗号拼接 + 自定义补充）', async () => {
    const card = reactive(makeCard({
      multi: true,
      status: 'answered',
      selected: ['a', 'b'],
      customText: '补充灰度',
    }));
    const m = await mountVisible(card);
    expect(m.el.textContent).toContain('方案 A, 方案 B — 补充灰度');
    expect(m.el.textContent).not.toContain('、');
  });
});
