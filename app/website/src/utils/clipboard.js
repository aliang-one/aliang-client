// 剪贴板写入兜底:http 局域网直连（56431 无 TLS）下 navigator.clipboard
// 不存在（Clipboard API 仅安全上下文可用）,裸调 writeText 会抛
// "Cannot read properties of undefined (reading 'writeText')"。本工具先探
// clipboard 的存在性,缺失即退 execCommand;两者都尽力而为,统一以布尔
// 结果返回,调用方据此给成功/失败反馈,永不向外抛错。
// （与 SshIngressSettings.vue 的本地 copyText 同语义;该处与 ChatMarkdown/
// SubagentDetail 的本地副本维持原样未动,新调用点一律走本工具。）
export function copyText(text) {
  if (typeof navigator !== 'undefined' && navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    return navigator.clipboard.writeText(text).then(() => true, () => false);
  }
  if (typeof document === 'undefined') {
    return Promise.resolve(false);
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (_) { /* 尽力而为 */ }
  ta.remove();
  return Promise.resolve(ok);
}
