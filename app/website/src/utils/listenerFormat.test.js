import { describe, it, expect } from 'vitest';
import {
  LISTENER_CLOUD_OFFLINE,
  isListenerCloudOffline,
  listenerStatusBadge,
  formatListenerRow,
  formatRelativeTime,
  normalizeListenerList,
} from './listenerFormat';

// 结构化 t 桩:返回 key(+params JSON),键名与插值参数一次断言两不漏。
const keyT = (key, params) => (params ? `${key}|${JSON.stringify(params)}` : key);

// 仿真 t 桩:按 vue-i18n {name} 命名插值渲染,断言最终人类可读文案。
const ZH_MESSAGES = {
  listener_justNow: '刚刚',
  listener_minutesAgo: '{minutes} 分钟前',
  listener_hoursAgo: '{hours} 小时前',
  listener_daysAgo: '{days} 天前',
};
const zhT = (key, params = {}) =>
  (ZH_MESSAGES[key] ?? key).replace(
    /\{(\w+)\}/g,
    (_, name) => (name in params ? String(params[name]) : `{${name}}`),
  );

// 本地时区安全的固定锚点:不带 Z 的 ISO 按本地时间解析,now/目标两侧同侧,
// 断言在任何时区都确定。
const NOW = Date.parse('2026-11-21T12:00:00');
const ago = (ms) => new Date(NOW - ms).toISOString();

describe('LISTENER_CLOUD_OFFLINE', () => {
  it('哨兵常量为 listener_cloud_offline(与后端 502 body code 一致)', () => {
    expect(LISTENER_CLOUD_OFFLINE).toBe('listener_cloud_offline');
  });
});

describe('listenerStatusBadge', () => {
  it('五个已知状态映射到键名+色调', () => {
    expect(listenerStatusBadge('active')).toEqual({ key: 'listener_status_active', tone: 'ok' });
    expect(listenerStatusBadge('paused')).toEqual({ key: 'listener_status_paused', tone: 'muted' });
    expect(listenerStatusBadge('fired')).toEqual({ key: 'listener_status_fired', tone: 'warn' });
    expect(listenerStatusBadge('expired')).toEqual({ key: 'listener_status_expired', tone: 'muted' });
    expect(listenerStatusBadge('error')).toEqual({ key: 'listener_status_error', tone: 'error' });
  });
  it('未知/缺失状态兜底 unknown + muted', () => {
    expect(listenerStatusBadge('weird')).toEqual({ key: 'listener_status_unknown', tone: 'muted' });
    expect(listenerStatusBadge(undefined)).toEqual({ key: 'listener_status_unknown', tone: 'muted' });
    expect(listenerStatusBadge(null)).toEqual({ key: 'listener_status_unknown', tone: 'muted' });
  });
});

describe('formatRelativeTime', () => {
  it('缺值/非法时间返回空串(调用方负责渲染 —)', () => {
    expect(formatRelativeTime(null, NOW, keyT)).toBe('');
    expect(formatRelativeTime('', NOW, keyT)).toBe('');
    expect(formatRelativeTime('not-a-date', NOW, keyT)).toBe('');
  });
  it('<60s 与未来时间戳(时钟偏移防抖)都算刚刚', () => {
    expect(formatRelativeTime(ago(30 * 1000), NOW, keyT)).toBe('listener_justNow');
    expect(formatRelativeTime(ago(0), NOW, keyT)).toBe('listener_justNow');
    // 未来 30s:Math.max(0,…) 钳制成刚刚(与仓内 ChatMessageBubble 同契约)
    expect(formatRelativeTime(ago(-30 * 1000), NOW, keyT)).toBe('listener_justNow');
  });
  it('分钟/小时/天三档带插值参数', () => {
    expect(formatRelativeTime(ago(5 * 60 * 1000), NOW, keyT)).toBe('listener_minutesAgo|{"minutes":5}');
    // 恰好 60s 边界归分钟档
    expect(formatRelativeTime(ago(60 * 1000), NOW, keyT)).toBe('listener_minutesAgo|{"minutes":1}');
    expect(formatRelativeTime(ago(3 * 60 * 60 * 1000), NOW, keyT)).toBe('listener_hoursAgo|{"hours":3}');
    expect(formatRelativeTime(ago(2 * 24 * 60 * 60 * 1000), NOW, keyT)).toBe('listener_daysAgo|{"days":2}');
  });
  it('超过 7 天降级为 M-D HH:mm 日期串(与 ChatMessageBubble 同形)', () => {
    const iso = ago(8 * 24 * 60 * 60 * 1000);
    const d = new Date(Date.parse(iso));
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    expect(formatRelativeTime(iso, NOW, keyT)).toBe(`${d.getMonth() + 1}-${d.getDate()} ${hh}:${mm}`);
  });
  it('仿真 t 桩渲染最终中文文案', () => {
    expect(formatRelativeTime(ago(5 * 60 * 1000), NOW, zhT)).toBe('5 分钟前');
    expect(formatRelativeTime(ago(30 * 1000), NOW, zhT)).toBe('刚刚');
  });
});

describe('formatListenerRow', () => {
  it('完整行:标题 + 状态线(状态·模式) + 相对时间', () => {
    const row = {
      id: 'ls_1',
      title: '盯 build.log 的 ERROR',
      status: 'active',
      mode: 'once',
      last_fired_at: ago(5 * 60 * 1000),
    };
    expect(formatListenerRow(row, keyT, NOW)).toEqual({
      title: '盯 build.log 的 ERROR',
      statusLine: 'listener_status_active · listener_mode_once',
      lastFiredText: 'listener_minutesAgo|{"minutes":5}',
    });
  });
  it('recurring 模式与未知模式(裸串透传)', () => {
    const base = { id: 'ls_2', title: 't', status: 'active', last_fired_at: null };
    expect(formatListenerRow({ ...base, mode: 'recurring' }, keyT, NOW).statusLine).toBe(
      'listener_status_active · listener_mode_recurring',
    );
    expect(formatListenerRow({ ...base, mode: 'weird' }, keyT, NOW).statusLine).toBe(
      'listener_status_active · weird',
    );
    expect(formatListenerRow({ ...base }, keyT, NOW).statusLine).toBe('listener_status_active');
  });
  it('error 行把 error_note 追加到状态线末尾', () => {
    const row = {
      id: 'ls_3',
      title: 't',
      status: 'error',
      mode: 'recurring',
      error_note: 'source file vanished',
      last_fired_at: ago(3 * 60 * 60 * 1000),
    };
    expect(formatListenerRow(row, keyT, NOW).statusLine).toBe(
      'listener_status_error · listener_mode_recurring · source file vanished',
    );
  });
  it('从未触发 last_fired_at=null 渲染 —', () => {
    const row = { id: 'ls_4', title: 't', status: 'paused', mode: 'once', last_fired_at: null };
    expect(formatListenerRow(row, keyT, NOW).lastFiredText).toBe('—');
  });
  it('缺 title 兜底 id;两者皆缺为空串', () => {
    expect(formatListenerRow({ id: 'ls_5', status: 'active' }, keyT, NOW).title).toBe('ls_5');
    expect(formatListenerRow({ status: 'active' }, keyT, NOW).title).toBe('');
  });
  it('脏输入(null 行)不崩:未知状态 + —', () => {
    expect(formatListenerRow(null, keyT, NOW)).toEqual({
      title: '',
      statusLine: 'listener_status_unknown',
      lastFiredText: '—',
    });
  });
});

describe('isListenerCloudOffline', () => {
  it('命中后端 502 现行 msg 文案(agentApi request() 抛的是 envelope.msg)', () => {
    expect(
      isListenerCloudOffline(
        new Error('listener cloud unreachable (offline, not registered or timed out)'),
      ),
    ).toBe(true);
  });
  it('命中哨兵子串(后端把 code 透进 msg 之日即自动生效)', () => {
    expect(isListenerCloudOffline(new Error(`xx ${LISTENER_CLOUD_OFFLINE} yy`))).toBe(true);
  });
  it('云端业务错误/普通失败不误判为离线', () => {
    expect(isListenerCloudOffline(new Error('listener_validation_error'))).toBe(false);
    expect(isListenerCloudOffline(new Error('Request failed with HTTP 500'))).toBe(false);
    expect(isListenerCloudOffline(new Error(''))).toBe(false);
  });
  it('非 Error 输入安全返回 false', () => {
    expect(isListenerCloudOffline(null)).toBe(false);
    expect(isListenerCloudOffline(undefined)).toBe(false);
    expect(isListenerCloudOffline('listener_cloud_offline')).toBe(false);
    expect(isListenerCloudOffline({})).toBe(false);
  });
});

describe('normalizeListenerList', () => {
  it('null/undefined/非数组归一为空数组', () => {
    expect(normalizeListenerList(null)).toEqual([]);
    expect(normalizeListenerList(undefined)).toEqual([]);
    expect(normalizeListenerList({ listeners: [] })).toEqual([]);
    expect(normalizeListenerList('nope')).toEqual([]);
  });
  it('数组原样透传(同引用)', () => {
    const arr = [{ id: 'ls_1' }];
    expect(normalizeListenerList(arr)).toBe(arr);
    expect(normalizeListenerList([])).toEqual([]);
  });
});
