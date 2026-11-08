import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock 掉——只测本文件
// 的 resolved 翻卡纯函数，不碰 composable 装配/网络。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
}));

import { resolveToolAskCard } from './useAliangChat';

const NOW = 1700000000000;

function makeConv() {
  return {
    aiSessionId: 'local-abc',
    toolAsks: [
      {
        key: 'local-abc:ta_1',
        interactionId: 'ta_1',
        sessionId: 'local-abc',
        messageId: '',
        provider: 'tool_ask',
        toolName: 'mcp__aliang__ask_user',
        title: '部署到哪个环境？',
        options: [
          { id: 'staging', label: 'staging' },
          { id: 'prod', label: 'prod' },
        ],
        allowCustom: true,
        multi: false,
        expiresAt: NOW + 60_000,
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

describe('resolveToolAskCard（决策跨端同步：resolved 同型帧翻卡）', () => {
  let handled;
  beforeEach(() => {
    handled = new Set();
  });

  it('别端作答翻卡：置 answered + selected（单选）+ customText，封键位', () => {
    const conv = makeConv();
    const applied = resolveToolAskCard(
      conv,
      {
        type: 'tool_ask.request',
        status: 'resolved',
        session_id: 'local-abc',
        interaction_id: 'ta_1',
        answer: { selected_option_id: 'staging' },
      },
      handled,
      NOW,
    );
    expect(applied).toBe(true);
    const card = conv.toolAsks[0];
    expect(card.status).toBe('answered');
    expect(card.selected).toEqual(['staging']);
    expect(card.error).toBe('');
    expect(card.updatedAt).toBe(NOW);
    expect(handled.has('local-abc:ta_1')).toBe(true);
  });

  it('多选作答经 answer.selected_option_ids 回填声明顺序', () => {
    const conv = makeConv();
    conv.toolAsks[0].multi = true;
    resolveToolAskCard(
      conv,
      {
        type: 'tool_ask.request',
        status: 'resolved',
        session_id: 'local-abc',
        interaction_id: 'ta_1',
        answer: { selected_option_ids: ['staging', 'prod'], message: '都要' },
      },
      handled,
      NOW,
    );
    const card = conv.toolAsks[0];
    expect(card.selected).toEqual(['staging', 'prod']);
    expect(card.customText).toBe('都要');
  });

  it('本地已作答键位幂等吸收：resolved 迟到重投不覆盖本地答案', () => {
    const conv = makeConv();
    conv.toolAsks[0].status = 'answered';
    conv.toolAsks[0].selected = ['prod'];
    handled.add('local-abc:ta_1');
    const applied = resolveToolAskCard(
      conv,
      {
        type: 'tool_ask.request',
        status: 'resolved',
        session_id: 'local-abc',
        interaction_id: 'ta_1',
        answer: { selected_option_id: 'staging' },
      },
      handled,
      NOW,
    );
    expect(applied).toBe(false);
    expect(conv.toolAsks[0].status).toBe('answered');
    expect(conv.toolAsks[0].selected).toEqual(['prod']);
    expect(conv.toolAsks[0].updatedAt).toBe(1);
  });

  it('无对应卡（迟到/未知 interaction）安全忽略，不建新卡', () => {
    const conv = makeConv();
    const applied = resolveToolAskCard(
      conv,
      {
        type: 'tool_ask.request',
        status: 'resolved',
        session_id: 'local-abc',
        interaction_id: 'ta_unknown',
        answer: { selected_option_id: 'staging' },
      },
      handled,
      NOW,
    );
    expect(applied).toBe(false);
    expect(conv.toolAsks).toHaveLength(1);
    expect(conv.toolAsks[0].status).toBe('pending');
    expect(handled.has('local-abc:ta_unknown')).toBe(false);
  });

  it('interaction_id 缺失/空串防御性返回', () => {
    const conv = makeConv();
    expect(resolveToolAskCard(conv, { type: 'tool_ask.request', status: 'resolved' }, handled, NOW)).toBe(false);
    expect(resolveToolAskCard(conv, { type: 'tool_ask.request', status: 'resolved', interaction_id: '  ' }, handled, NOW)).toBe(false);
    expect(conv.toolAsks).toHaveLength(1);
  });
});
