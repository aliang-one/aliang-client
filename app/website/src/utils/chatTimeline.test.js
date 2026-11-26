import { describe, it, expect } from 'vitest';
import { mergeTimeline } from './chatTimeline';

describe('mergeTimeline', () => {
  it('按 ts 升序归并 messages 与 steerItems 并打 type 标', () => {
    const out = mergeTimeline(
      [{ role: 'user', content: 'a', ts: 200 }, { role: 'assistant', content: 'b', ts: 300 }],
      [{ messageId: 's1', content: 'x', ts: 250 }],
    );
    expect(out.map(i => i.type)).toEqual(['message', 'steer', 'message']);
  });
  it('缺 ts 的项排最后(防御)', () => {
    const out = mergeTimeline([{ role: 'user', content: 'a' }], []);
    expect(out).toHaveLength(1);
  });
  it('两个都无 ts 的项保持 concat 序(比较器 NaN 防御)', () => {
    const out = mergeTimeline([{ role: 'user', content: 'a' }], [{ messageId: 's1', content: 'x' }]);
    expect(out.map(i => [i.type, i.content])).toEqual([['message', 'a'], ['steer', 'x']]);
  });
  it('脏输入安全返回空数组', () => {
    expect(mergeTimeline(null, undefined)).toEqual([]);
  });

  // 2026-11-26 卡片内联(症状2修复):交互卡按 createdAt 与消息同轨排序,
  // 落在发起它的请求泡之后——不再整体钉列表底。
  it('卡片按 ts 内联进时间线并打 card 标,落发起泡之后', () => {
    const out = mergeTimeline(
      [
        { role: 'user', content: 'q', ts: 100, messageId: 'm1' },
        { role: 'assistant', content: 'ans', ts: 200 },
        { role: 'user', content: 'q2', ts: 400, messageId: 'm2' },
        { role: 'assistant', content: 'ans2', ts: 500 },
      ],
      [],
      [{ key: 'k1', optionId: 'opt1', createdAt: 200, status: 'answered' }],
    );
    expect(out.map(i => i.type)).toEqual(['message', 'message', 'card', 'message', 'message']);
    expect(out[2].key).toBe('k1');
  });
  it('同 ts 时卡按 concat 序排消息之后(发起泡与卡几乎同刻)', () => {
    const out = mergeTimeline(
      [{ role: 'assistant', content: 'ans', ts: 200 }],
      [],
      [{ key: 'k1', createdAt: 200 }],
    );
    expect(out.map(i => i.type)).toEqual(['message', 'card']);
  });
  it('无 ts 的卡排最后(保持旧钉底兜底,不丢卡)', () => {
    const out = mergeTimeline(
      [{ role: 'user', content: 'q', ts: 100 }],
      [],
      [{ key: 'k1' }],
    );
    expect(out.map(i => i.type)).toEqual(['message', 'card']);
    expect(out[1].key).toBe('k1');
  });
  it('cards 脏输入安全;输入数组不被改写', () => {
    const cards = [{ key: 'k1', createdAt: 1 }];
    mergeTimeline([{ role: 'user', content: 'a', ts: 2 }], [], cards);
    expect(cards).toEqual([{ key: 'k1', createdAt: 1 }]);
    expect(mergeTimeline(null, null, null)).toEqual([]);
  });
});

import { chatTurnBareId } from './chatTimeline';

describe('chatTurnBareId（回合 id 归一）', () => {
  it('剥 assistant_ 前缀与 .option 后缀', () => {
    expect(chatTurnBareId('assistant_abc.option')).toBe('abc');
    expect(chatTurnBareId('assistant_abc')).toBe('abc');
    expect(chatTurnBareId('abc')).toBe('abc');
    expect(chatTurnBareId('')).toBe('');
    expect(chatTurnBareId(null)).toBe('');
  });
});

import { resolveCardAnchor } from './chatTimeline';

describe('resolveCardAnchor（messageId 锚定 + clamp）', () => {
  const msgs = [
    { role: 'user', content: 'q1', messageId: 'm1', ts: 100 },
    { role: 'assistant', content: 'ans1', ts: 200 },
    { role: 'user', content: 'q2', messageId: 'm2', ts: 400 },
    { role: 'assistant', content: 'ans2', ts: 500 },
  ];

  it('锚中:卡落回合段内(下限=段内最大 ts)', () => {
    const r = resolveCardAnchor({ messageId: 'assistant_m1.option', createdAt: 50 }, msgs);
    expect(r.anchored).toBe(true);
    expect(r.ts).toBe(200); // clamp 到段尾
  });
  it('上限:迟到卡不得越过后继 user 泡', () => {
    const r = resolveCardAnchor({ messageId: 'm1', createdAt: 99999 }, msgs);
    expect(r.ts).toBe(399);
  });
  it('无后继 user 泡:落会话尾(段尾与 createdAt 取大)', () => {
    const r = resolveCardAnchor({ messageId: 'm2', createdAt: 600 }, msgs.slice(2));
    expect(r.ts).toBe(600);
  });
  it('锚不中(无 id/反查不中):anchored=false 回落 createdAt', () => {
    expect(resolveCardAnchor({ createdAt: 123 }, msgs).anchored).toBe(false);
    expect(resolveCardAnchor({ messageId: 'ghost', createdAt: 123 }, msgs).anchored).toBe(false);
    expect(resolveCardAnchor(null, msgs).anchored).toBe(false);
  });
  it('mergeTimeline 接入锚定:卡插在发起回合段与后继回合之间', () => {
    const out = mergeTimeline(
      [
        { role: 'user', content: 'q1', messageId: 'm1', ts: 100 },
        { role: 'assistant', content: 'ans1', ts: 200 },
        { role: 'user', content: 'q2', messageId: 'm2', ts: 400 },
      ],
      [],
      [{ key: 'k1', messageId: 'assistant_m1', createdAt: 99999, status: 'answered' }],
    );
    expect(out.map(i => i.type)).toEqual(['message', 'message', 'card', 'message']);
  });
});
