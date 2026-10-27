import { describe, expect, it } from 'vitest';
import { CHAT_TITLE_MAX_LENGTH, normalizeConversationTitle } from './chatTitle';

describe('normalizeConversationTitle', () => {
  it('空值/纯空白返回空串（由渲染期兜底显示「新对话」）', () => {
    expect(normalizeConversationTitle('')).toBe('');
    expect(normalizeConversationTitle('   ')).toBe('');
    expect(normalizeConversationTitle('\n\t ')).toBe('');
    expect(normalizeConversationTitle(null)).toBe('');
    expect(normalizeConversationTitle(undefined)).toBe('');
  });

  it('剥离首尾空白', () => {
    expect(normalizeConversationTitle('  修复登录  ')).toBe('修复登录');
  });

  it('折叠内部换行/连续空白为单个空格', () => {
    expect(normalizeConversationTitle('第一行\n第二行')).toBe('第一行 第二行');
    expect(normalizeConversationTitle('a    b')).toBe('a b');
  });

  it('超过上限截断到 60 字符', () => {
    const raw = 'x'.repeat(80);
    const out = normalizeConversationTitle(raw);
    expect(out).toHaveLength(CHAT_TITLE_MAX_LENGTH);
    expect(CHAT_TITLE_MAX_LENGTH).toBe(60);
  });

  it('非字符串输入按字符串处理', () => {
    expect(normalizeConversationTitle(2026)).toBe('2026');
  });
});
