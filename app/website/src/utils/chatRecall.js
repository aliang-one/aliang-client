// 撤回重发纯逻辑(spec §A):切片、入口判定、撤回 create 载荷。
// 铁律:撤回帧绝不携带 transcript(宿主从 journal 全文重建),也绝不携带
// resume_session_id(resume 仅首次 create 的既有契约)。
import { sessionCreatePayload } from './chatSettings';

// 返回目标消息之前的切片;目标不存在返回 null(禁用撤回入口)。
export function sliceMessagesBefore(messages, messageId) {
  if (!Array.isArray(messages) || typeof messageId !== 'string' || !messageId) return null;
  const idx = messages.findIndex(m => m && m.messageId === messageId);
  if (idx < 0) return null;
  return messages.slice(0, idx);
}

// 撤回入口判定(spec §A provider 门控):仅 agentcore 会话的 user 消息,
// 且该消息有 messageId(存量旧消息无 id 只能复制)。
export function canRecall(msg) {
  return !!msg
    && msg.role === 'user'
    && typeof msg.messageId === 'string' && !!msg.messageId
    && msg.provider === 'agentcore';
}

// 撤回 create 载荷:常规载荷打底,删 transcript/resume,加 truncate 键。
export function recallCreatePayload(conv, beforeMessageId) {
  const payload = sessionCreatePayload(conv);
  delete payload.transcript;
  delete payload.resume_session_id;
  payload.truncate_before_message_id = String(beforeMessageId);
  return payload;
}
