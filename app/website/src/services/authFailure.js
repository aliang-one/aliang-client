function objectValue(value) {
  return value && typeof value === 'object' ? value : null;
}

function envelopeLayers(envelope) {
  const layers = [];
  let current = objectValue(envelope);
  for (let depth = 0; current && depth < 3; depth += 1) {
    layers.push(current);
    current = objectValue(current.data);
  }
  return layers;
}

function normalized(value) {
  return typeof value === 'string' ? value.trim().toLowerCase() : '';
}

// 明确拒绝语义：仅当 envelope 任一层携带这些证据时才判定会话失效。
// 注意后端两类真实 401 文案互不为子串，需分别列出：
// - dashboard/remote 中间件 "Authenticated dashboard session is required"
// - quick-setup 上游快照 "authenticated session is required"（ErrQuickSetupUnauthenticated）
const ACCESS_REJECTION_PHRASES = [
  'local session is no longer valid',
  'invalid authentication',
  'authenticated dashboard session is required',
  'authenticated session is required',
  'token has been revoked',
  'invalid access token',
  'token has expired',
  'token expired',
  'expired token',
  'token revoked',
  'session_expired',
  'token_expired',
];

export function isAuthenticationFailure(responseStatus, envelope) {
  // 裸 401/403 等状态码本身不再是失效证据（网关瞬时 401 不再触发登出
  // reconcile）。证据分两档：
  // - 结构性证据（status=unauthenticated / error / code 终态标记）不限状态码——
  //   nested terminal envelope（如 200 + data.status=unauthenticated）保持原语义；
  // - 自由文本短语（ACCESS_REJECTION_PHRASES）仅 401 可信：非 401 响应的
  //   msg 可能内嵌上游透传文本（如 "Failed to fetch profile: ...token expired..."），
  //   按短语误判会把网关刻意保留的瞬时故障翻成登出。
  const phrasesAreEvidence = Number(responseStatus) === 401;
  return envelopeLayers(envelope).some((layer) => {
    const status = normalized(layer.status);
    const error = normalized(layer.error);
    const code = normalized(layer.code);
    if (
      status === 'unauthenticated'
      || error === 'session_expired'
      || error === 'token_expired'
      || code === 'token_expired'
      || code === 'token_revoked'
    ) {
      return true;
    }
    if (!phrasesAreEvidence) {
      return false;
    }
    const message = normalized(layer.message || layer.msg);
    return ACCESS_REJECTION_PHRASES.some((phrase) => message.includes(phrase));
  });
}

export function authenticationFailureMessage(envelope) {
  for (const layer of envelopeLayers(envelope)) {
    for (const field of ['msg', 'message', 'error']) {
      if (typeof layer[field] === 'string' && layer[field].trim()) {
        return layer[field].trim();
      }
    }
  }
  return '';
}

export function handleAuthenticationFailure(responseStatus, envelope, onFailure) {
  if (!isAuthenticationFailure(responseStatus, envelope)) {
    return false;
  }
  if (typeof onFailure === 'function') {
    onFailure(authenticationFailureMessage(envelope));
  }
  return true;
}
