import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { chatMessageSeq, orderMessagesBySeq } from '../useAliangChat.js';

// DEBT-05 共享回归夹具:同一批乱序/重复/旧快照样例,手机与网页必须收敛到
// 相同顺序。权威源在 docs/superpowers/fixtures/message-order-cases.json,
// RN 仓 src/utils/__fixtures__/ 是逐字节副本——改一处要同步另一处。
const cases = JSON.parse(
  readFileSync(
    resolve(__dirname, '../../../../../docs/superpowers/fixtures/message-order-cases.json'),
    'utf8',
  ),
).cases;

describe('message order contract (shared with RN)', () => {
  for (const tc of cases) {
    it(tc.name, () => {
      const before = tc.input.map(m => m.id);
      const snapshot = JSON.stringify(tc.input);
      const out = orderMessagesBySeq(tc.input);
      expect(out.map(m => m.id)).toEqual(tc.expected);
      // 不改原数组:排序不得产生副作用。
      expect(JSON.stringify(tc.input)).toBe(snapshot);
      expect(tc.input.map(m => m.id)).toEqual(before);
    });
  }

  it('chatMessageSeq: 0/负数/缺字段/非数字一律视为不可排序', () => {
    expect(chatMessageSeq({ seq: 5 })).toBe(5);
    expect(chatMessageSeq({ seq: 0 })).toBe(0);
    expect(chatMessageSeq({ seq: -1 })).toBe(0);
    expect(chatMessageSeq({})).toBe(0);
    expect(chatMessageSeq({ seq: '7' })).toBe(0);
    expect(chatMessageSeq(undefined)).toBe(0);
  });

  it('无可排序内容时原样返回入参(引用相等),避免无谓重渲染', () => {
    const allPlain = [{ id: 'a' }, { id: 'b' }];
    expect(orderMessagesBySeq(allPlain)).toBe(allPlain);
    const single = [{ id: 'solo', seq: 1 }, { id: 'plain' }];
    expect(orderMessagesBySeq(single)).toBe(single);
  });
});
