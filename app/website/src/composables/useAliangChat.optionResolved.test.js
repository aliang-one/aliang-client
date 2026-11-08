import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock 掉——只测本文件
// 的 resolved 翻卡纯函数，不碰 composable 装配/网络。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
}));

import { resolveOptionCard } from './useAliangChat';

const NOW = 1700000000000;

function makeConv() {
  return {
    aiSessionId: 'local-abc',
    optionCards: [
      {
        key: 'local-abc:opt_1',
        optionId: 'opt_1',
        sessionId: 'local-abc',
        messageId: 'm_asst',
        provider: 'claude',
        title: '选方案',
        options: [
          { id: 'a', label: 'A' },
          { id: 'b', label: 'B' },
        ],
        allowCustom: false,
        multi: false,
        status: 'pending',
        selected: [],
        customText: '',
        error: '',
        sourceId: '',
        updatedAt: 1,
      },
    ],
  };
}

describe('resolveOptionCard（决策跨端同步：resolved 同型帧翻卡）', () => {
  let handled;
  beforeEach(() => {
    handled = new Set();
  });

  it('别端作答翻卡：置 answered + selected + customText，封键位', () => {
    const conv = makeConv();
    const applied = resolveOptionCard(
      conv,
      {
        type: 'ai.option.request',
        status: 'resolved',
        session_id: 'local-abc',
        option_id: 'opt_1',
        selected: ['b'],
        custom_text: '备注',
        message_id: 'm_asst',
      },
      handled,
      NOW,
    );
    expect(applied).toBe(true);
    const card = conv.optionCards[0];
    expect(card.status).toBe('answered');
    expect(card.selected).toEqual(['b']);
    expect(card.customText).toBe('备注');
    expect(card.error).toBe('');
    expect(card.updatedAt).toBe(NOW);
    expect(handled.has('local-abc:opt_1')).toBe(true);
  });

  it('本地已作答键位幂等吸收：resolved 迟到重投不覆盖本地答案', () => {
    const conv = makeConv();
    conv.optionCards[0].status = 'answered';
    conv.optionCards[0].selected = ['a'];
    handled.add('local-abc:opt_1');
    const applied = resolveOptionCard(
      conv,
      {
        type: 'ai.option.request',
        status: 'resolved',
        session_id: 'local-abc',
        option_id: 'opt_1',
        selected: ['b'],
      },
      handled,
      NOW,
    );
    expect(applied).toBe(false);
    expect(conv.optionCards[0].status).toBe('answered');
    expect(conv.optionCards[0].selected).toEqual(['a']);
    expect(conv.optionCards[0].updatedAt).toBe(1);
  });

  it('无对应卡（迟到/未知 id）安全忽略，不建新卡', () => {
    const conv = makeConv();
    const applied = resolveOptionCard(
      conv,
      {
        type: 'ai.option.request',
        status: 'resolved',
        session_id: 'local-abc',
        option_id: 'opt_unknown',
        selected: ['a'],
      },
      handled,
      NOW,
    );
    expect(applied).toBe(false);
    expect(conv.optionCards).toHaveLength(1);
    expect(conv.optionCards[0].status).toBe('pending');
    expect(handled.has('local-abc:opt_unknown')).toBe(false);
  });

  it('option_id 缺失/空串防御性返回', () => {
    const conv = makeConv();
    expect(resolveOptionCard(conv, { type: 'ai.option.request', status: 'resolved' }, handled, NOW)).toBe(false);
    expect(resolveOptionCard(conv, { type: 'ai.option.request', status: 'resolved', option_id: '  ' }, handled, NOW)).toBe(false);
    expect(conv.optionCards).toHaveLength(1);
  });
});
