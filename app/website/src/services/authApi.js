function objectValue(value) {
  return value && typeof value === 'object' ? value : {};
}

function extractEnvelope(json) {
  const root = objectValue(json);
  const payload = objectValue(root.data);
  return {
    code: typeof root.code === 'number' ? root.code : 0,
    msg: typeof root.msg === 'string' ? root.msg : '',
    status: typeof payload.status === 'string' ? payload.status : '',
    error: typeof payload.error === 'string' ? payload.error : '',
    message: typeof payload.msg === 'string' ? payload.msg : '',
    data: payload.data && typeof payload.data === 'object' ? payload.data : null,
    payload
  };
}

export class AuthRequestError extends Error {
  constructor(message, { type = 'server_error', status = 0, code = '' } = {}) {
    super(message);
    this.name = 'AuthRequestError';
    this.type = type;
    this.status = status;
    this.code = code;
  }
}

function classifyStructuredError(envelope) {
  const code = String(envelope.error || envelope.payload?.outcome || '').trim().toLowerCase();
  if (code === 'session_recovering') return 'session_recovering';
  if (code === 'session_invalid' || code === 'session_expired' || code === 'token_expired') {
    return 'session_invalid';
  }
  return 'server_error';
}

async function request(path, options = {}, timeoutMs) {
  const controller = new AbortController();
  const timer = timeoutMs ? setTimeout(() => controller.abort(), timeoutMs) : null;
  let response;
  try {
    response = await fetch(path, {
      credentials: 'same-origin',
      ...options,
      headers: {
        'Content-Type': 'application/json',
        ...(options.headers || {})
      },
      signal: controller.signal
    });
  } catch (error) {
    const message = error?.name === 'AbortError'
      ? `Request to ${path} timed out after ${timeoutMs}ms`
      : (error instanceof Error ? error.message : 'Backend transport unavailable');
    throw new AuthRequestError(message, { type: 'transport_unavailable' });
  } finally {
    if (timer) clearTimeout(timer);
  }

  const envelope = extractEnvelope(await response.json().catch(() => ({})));
  if (!response.ok || envelope.code !== 0) {
    throw new AuthRequestError(
      envelope.message || envelope.msg || `Request failed with HTTP ${response.status}`,
      {
        type: classifyStructuredError(envelope),
        status: response.status,
        code: envelope.error
      }
    );
  }
  return envelope;
}

export async function bootstrapDashboardSession() {
  return request('/api/dashboard/session', { method: 'POST', body: JSON.stringify({}) }, 5000);
}

export async function login({ email, password }) {
  return request('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify({ email, password })
  });
}

export async function restoreSession() {
  const envelope = await request('/api/auth/session', { method: 'GET' }, 5000);
  return envelope.payload;
}

export async function logout() {
  return request('/api/auth/logout', { method: 'POST', body: JSON.stringify({}) });
}

export async function scanInit() {
  return request('/api/auth/scan/init', { method: 'POST', body: JSON.stringify({}) });
}

export async function scanStatus(deviceCode) {
  const query = new URLSearchParams({ device_code: deviceCode }).toString();
  return request(`/api/auth/scan/status?${query}`, { method: 'GET' });
}

export async function activateScanLogin({ sessionToken, refreshToken }) {
  return request('/api/auth/scan/activate', {
    method: 'POST',
    body: JSON.stringify({ session_token: sessionToken, refresh_token: refreshToken })
  });
}
