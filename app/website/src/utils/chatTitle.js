export const CHAT_TITLE_MAX_LENGTH = 60;

// 会话标题归一化。空串是合法结果：渲染期用 `c.title || t('chat_emptyTitle')`
// 兜底，绝不把某个语言的译文写进 localStorage（否则切语言后标题不跟随）。
export function normalizeConversationTitle(raw) {
  const text = String(raw ?? '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.slice(0, CHAT_TITLE_MAX_LENGTH);
}

export function canRenameConversation(conversation) {
  if (!conversation) return false;
  const id = conversation.deviceSession?.id || conversation.aiSessionId || '';
  return !/^(goal_|claude_|codex_)/.test(id);
}

// Agent titles use UTC RFC3339Nano. Compare the fractional remainder when two
// renames fall within the same JavaScript millisecond.
export function applyConversationTitle(conversation, title, stamp) {
  const incoming = Date.parse(stamp);
  const previous = Date.parse(conversation.titleUpdatedAt);
  if (!title || !Number.isFinite(incoming) || incoming < previous) return false;
  if (incoming === previous) {
    const fraction = value => ((value.match(/\.(\d+)Z$/)?.[1] ?? '').padEnd(9, '0'));
    if (fraction(stamp) <= fraction(conversation.titleUpdatedAt)) return false;
  }
  conversation.title = title;
  conversation.titleUpdatedAt = stamp;
  return true;
}
