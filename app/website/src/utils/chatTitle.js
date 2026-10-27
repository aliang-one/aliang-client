export const CHAT_TITLE_MAX_LENGTH = 60;

// 会话标题归一化。空串是合法结果：渲染期用 `c.title || t('chat_emptyTitle')`
// 兜底，绝不把某个语言的译文写进 localStorage（否则切语言后标题不跟随）。
export function normalizeConversationTitle(raw) {
  const text = String(raw ?? '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.slice(0, CHAT_TITLE_MAX_LENGTH);
}
