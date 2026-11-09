import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock 掉——只测本文件
// 的转录映射纯函数，不碰 composable 装配/网络。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
}));

import {
  deviceDetailTranscriptToMessages,
  deviceSnapshotTranscriptToMessages,
  deviceSystemNoteBubble,
} from './useAliangChat';

const FALLBACK_TS = 1700000000000;

describe('deviceSystemNoteBubble', () => {
  it('镜像仓内系统条形状（审批取消/运行提示条同族）', () => {
    const bubble = deviceSystemNoteBubble('这一轮执行失败：上游失联', 123);
    expect(bubble).toEqual({
      role: 'assistant',
      kind: 'system',
      note: 'turn_failed',
      content: '这一轮执行失败：上游失联',
      status: 'done',
      ts: 123,
    });
  });
});

describe('deviceDetailTranscriptToMessages（详情转录）', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('user/assistant 落气泡并进喂入，system 只落气泡不进喂入', () => {
    const rows = [
      { role: 'user', content: '查一下', id: 'u1', timestamp: '2026-11-05T01:00:05Z' },
      { role: 'assistant', content: '结果', id: 'a1', timestamp: '2026-11-05T01:00:09Z' },
      // G5：server 读侧把 journal end(error) 折出的错误终态系统条。
      { role: 'system', content: '这一轮执行失败：工具连续 3 次执行失败，疑似陷入重试循环', id: 'end-000001', timestamp: '2026-11-05T01:00:10Z' },
      // tool 轨迹行继续跳过。
      { role: 'tool', content: 'Bash ls', id: 'tool-000001' },
    ];
    const out = deviceDetailTranscriptToMessages(rows, 'agentcore', FALLBACK_TS);
    expect(out.messages).toHaveLength(3);
    expect(out.messages[0]).toMatchObject({ role: 'user', content: '查一下', messageId: 'u1', provider: 'agentcore' });
    expect(out.messages[1]).toMatchObject({ role: 'assistant', content: '结果', messageId: 'a1' });
    expect(out.messages[2]).toEqual({
      role: 'assistant',
      kind: 'system',
      note: 'turn_failed',
      content: '这一轮执行失败：工具连续 3 次执行失败，疑似陷入重试循环',
      status: 'done',
      ts: Date.parse('2026-11-05T01:00:10Z'),
    });
    // 喂入流（续聊模型上下文）绝不含系统条——事实说明喂回去会污染。
    expect(out.feed).toEqual([
      { role: 'user', content: '查一下' },
      { role: 'assistant', content: '结果' },
    ]);
  });

  it('时间戳解析失败回落 fallbackTs；脏行安全跳过', () => {
    const rows = [
      { role: 'system', content: '这一轮执行失败', timestamp: 'not-a-date' },
      null,
      { role: 'user' },
      { role: 'user', content: '' },
    ];
    const out = deviceDetailTranscriptToMessages(rows, '', FALLBACK_TS);
    expect(out.messages).toHaveLength(1);
    expect(out.messages[0].ts).toBe(FALLBACK_TS);
    expect(out.feed).toEqual([]);
  });
});

describe('deviceSnapshotTranscriptToMessages（快照转录）', () => {
  it('text 字段映射，system 行落系统条（快照本就不写喂入流）', () => {
    const rows = [
      { role: 'user', text: '查一下', message_id: 'u1', ts: '2026-11-05T01:00:05Z' },
      { role: 'system', text: '这一轮执行失败：平台 AI 代理未运行(127.0.0.1:56432 连接被拒,已尝试自动拉起)——请在设备仪表盘启动代理后重试', ts: '2026-11-05T01:00:07Z' },
    ];
    const out = deviceSnapshotTranscriptToMessages(rows, 'agentcore', FALLBACK_TS);
    expect(out.messages).toHaveLength(2);
    expect(out.messages[0]).toMatchObject({ role: 'user', content: '查一下', messageId: 'u1' });
    expect(out.messages[1]).toEqual({
      role: 'assistant',
      kind: 'system',
      note: 'turn_failed',
      content: '这一轮执行失败：平台 AI 代理未运行(127.0.0.1:56432 连接被拒,已尝试自动拉起)——请在设备仪表盘启动代理后重试',
      status: 'done',
      ts: Date.parse('2026-11-05T01:00:07Z'),
    });
  });

  it('非数组/空列表安全', () => {
    expect(deviceSnapshotTranscriptToMessages(null, '', FALLBACK_TS).messages).toEqual([]);
  });
});

// 2026-11-09 审计 REAL-high:快照/详情整体替换把气泡全置 done,journal 快照
// 天然缺在途回合内容(turn 行回合末才落),替换后后续 delta 命中
// ensureAssistantBubble 的 done 屏障被静默吞掉(表现为答案消失)。会话仍在跑
// 时,替换结果必须补一枚尾部 running 占位,让 delta 有落点。
describe('running 尾占位（快照/详情替换吞流守卫）', () => {
  const detailRows = [
    { role: 'user', content: '问题', id: 'u1', timestamp: '2026-11-05T01:00:05Z' },
    { role: 'assistant', content: '上一答', id: 'a1', timestamp: '2026-11-05T01:00:09Z' },
  ];

  it('running=false：无占位（既有行为钉死）', () => {
    const out = deviceDetailTranscriptToMessages(detailRows, 'agentcore', FALLBACK_TS, false);
    expect(out.messages).toHaveLength(2);
    expect(out.messages.every(m => m.status === 'done')).toBe(true);
  });

  it('running=true（默认参数缺省）：无占位（回归保护——非 running 路径不得误补）', () => {
    const out = deviceDetailTranscriptToMessages(detailRows, 'agentcore', FALLBACK_TS);
    expect(out.messages).toHaveLength(2);
  });

  it('详情 running=true：尾部补 running 占位，delta 有落点', () => {
    const out = deviceDetailTranscriptToMessages(detailRows, 'agentcore', FALLBACK_TS, true);
    expect(out.messages).toHaveLength(3);
    expect(out.messages[2]).toEqual({ role: 'assistant', content: '', status: 'running', ts: FALLBACK_TS });
    // 占位绝不进喂入流。
    expect(out.feed).toEqual([
      { role: 'user', content: '问题' },
      { role: 'assistant', content: '上一答' },
    ]);
  });

  it('快照 running=true：尾部补 running 占位', () => {
    const srows = [{ role: 'user', text: '问题', message_id: 'u1', ts: '2026-11-05T01:00:05Z' }];
    const out = deviceSnapshotTranscriptToMessages(srows, 'agentcore', FALLBACK_TS, true);
    expect(out.messages).toHaveLength(2);
    expect(out.messages[1]).toEqual({ role: 'assistant', content: '', status: 'running', ts: FALLBACK_TS });
  });

  it('纯图行(content 空、带 attachments)保留进 messages、不进 feed（生产保真——内联实装同款,附件 URL 走 chatAttachmentUrl）', () => {
    const out = deviceDetailTranscriptToMessages(
      [{ role: 'user', content: '', id: 'u2', attachments: [{ name: 'a.png', ref: 'abc/def' }] }],
      'agentcore', FALLBACK_TS);
    expect(out.messages).toHaveLength(1);
    expect(out.messages[0]).toMatchObject({
      role: 'user',
      content: '',
      messageId: 'u2',
      attachments: [{ name: 'a.png', url: '/api/agent/ai/attachment?ref=abc%2Fdef' }],
    });
    expect(out.feed).toHaveLength(0);
  });
});

// 零 delta 回归（2026-11-09 纯函数收编验收）：收编前内联实装对 role=system
// 无条件落条（空正文也落，正文兜底 ''），收编后必须保持同形，避免行为漂移。
describe('system 空正文零 delta', () => {
  it('详情：空正文系统条仍落条，content 兜底为空串', () => {
    const out = deviceDetailTranscriptToMessages([{ role: 'system', timestamp: '2026-11-05T01:00:00Z' }], 'agentcore', FALLBACK_TS);
    expect(out.messages).toHaveLength(1);
    expect(out.messages[0]).toMatchObject({ role: 'assistant', kind: 'system', note: 'turn_failed', content: '', status: 'done' });
    expect(out.feed).toEqual([]);
  });

  it('快照：空正文系统条仍落条', () => {
    const out = deviceSnapshotTranscriptToMessages([{ role: 'system', ts: '2026-11-05T01:00:00Z' }], 'agentcore', FALLBACK_TS);
    expect(out.messages).toHaveLength(1);
    expect(out.messages[0]).toMatchObject({ kind: 'system', content: '' });
  });
});
