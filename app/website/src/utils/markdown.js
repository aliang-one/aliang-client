// dashboard 聊天气泡 markdown 渲染(2026-10-31):ChatPage 此前以裸文本插值
// `{{ msg.content }}` 渲染,assistant 回复里的标题/加粗/表格全部原样显示。
// 手机端(agentTranscript→messageRendering)一直是完整 markdown 解析,桌面侧
// 对齐为「assistant 气泡渲染 markdown、user 气泡保持原文」的同款契约。
//
// 安全:内容是模型输出,可含 <script>/<img onerror> 等注入面——marked 不做
// 消毒,必须过 DOMPurify;链接统一补 target=_blank + rel=noopener,防钓鱼
// 页面在面板内开新窗。
import { marked } from 'marked';
import DOMPurify from 'dompurify';

marked.setOptions({ gfm: true, breaks: true });

export function renderMarkdown(text) {
  if (!text || typeof text !== 'string') return '';
  const html = marked.parse(text);
  const clean = DOMPurify.sanitize(html, {
    ADD_ATTR: ['target'],
    FORBID_TAGS: ['style', 'form', 'input', 'iframe'],
  });
  return clean.replace(/<a /g, '<a target="_blank" rel="noopener noreferrer" ');
}
