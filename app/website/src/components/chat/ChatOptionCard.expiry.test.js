// tool_ask 过期置灰（2026-11-28 三审 F6）：expiresAt 此前只存不用
// （upsertToolAskCard 存、渲染链无人消费）——后端 SLA 定时 cancel 帧是清卡
// 权威，但漏帧窗口内过期卡仍可提交，迟答被前端乐观置 answered 而服务端
// deliver() 静默丢弃（UI/模型分叉）。修复=渲染层消费 expiresAt：过期置灰
// 提交键+选项与自定义输入，卡头标「已过期」；nowTick（30s）驱动重算，
// 无 expiresAt 的方案卡（ai.option）行为不变。
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
    title: '需要确认',
    header: '',
    summary: '请选择',
    options: [
      { id: 'a', label: '方案 A', description: '', response: '', previews: [] },
      { id: 'b', label: '方案 B', description: '', response: '', previews: [] },
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

function mountCard(card, nowTick = 0) {
  const el = document.createElement('div');
  const events = [];
  const app = createApp({
    render: () => h(ChatOptionCard, {
      card,
      nowTick,
      onRespond: (payload) => events.push(payload),
    }),
  });
  app.mount(el);
  return { el, events, app };
}

const submitBtn = (el) => el.querySelector('[data-testid="chat-option-submit"]');
const optBtn = (el, i = 0) => el.querySelectorAll('[role="radio"], [role="checkbox"]')[i];

async function selectFirst(card, el) {
  optBtn(el).click();
  await nextTick();
}

describe('ChatOptionCard 过期置灰（tool_ask expiresAt 消费）', () => {
  it('expiresAt 未到：可选可提交，无过期徽标', async () => {
    const card = reactive(makeCard({ expiresAt: Date.now() + 600000 }));
    const { el, events } = mountCard(card, Date.now());
    await selectFirst(card, el);
    expect(submitBtn(el).disabled).toBe(false);
    expect(el.textContent).not.toContain(t('chat_optionExpired'));
    submitBtn(el).click();
    await nextTick();
    expect(events).toHaveLength(1);
  });

  it('expiresAt 已过：选项/输入/提交全部置灰并标「已过期」', async () => {
    const card = reactive(makeCard({ expiresAt: Date.now() - 1000 }));
    const { el, events } = mountCard(card, Date.now());
    await nextTick();
    expect(el.textContent).toContain(t('chat_optionExpired'));
    expect(submitBtn(el).disabled).toBe(true);
    expect(optBtn(el).disabled).toBe(true);
    submitBtn(el).click();
    optBtn(el).click();
    await nextTick();
    expect(events).toHaveLength(0);
  });

  it('nowTick 推进跨过期点：卡片随之置灰（30s 重算）', async () => {
    const card = reactive(makeCard({ expiresAt: 1000 }));
    const state = reactive({ tick: 500 });
    const el = document.createElement('div');
    const events = [];
    const app = createApp({
      render: () => h(ChatOptionCard, {
        card,
        nowTick: state.tick,
        onRespond: (payload) => events.push(payload),
      }),
    });
    app.mount(el);
    await selectFirst(card, el);
    expect(submitBtn(el).disabled).toBe(false);
    state.tick = 2000;
    await nextTick();
    expect(el.textContent).toContain(t('chat_optionExpired'));
    expect(submitBtn(el).disabled).toBe(true);
    submitBtn(el).click();
    expect(events).toHaveLength(0);
  });

  it('无 expiresAt（ai.option 方案卡）：永不过期', async () => {
    const card = reactive(makeCard({}));
    const { el, events } = mountCard(card, Number.MAX_SAFE_INTEGER / 2);
    await selectFirst(card, el);
    expect(submitBtn(el).disabled).toBe(false);
    expect(el.textContent).not.toContain(t('chat_optionExpired'));
  });
});
