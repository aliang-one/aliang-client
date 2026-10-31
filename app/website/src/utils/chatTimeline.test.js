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
});
