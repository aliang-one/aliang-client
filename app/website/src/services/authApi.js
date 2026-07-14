function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  const data = payload?.data && typeof payload.data === 'object' ? payload.data : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    status: typeof data.status === 'string' ? data.status : '',
    error: typeof data.error === 'string' ? data.error : '',
    message: typeof data.msg === 'string' ? data.msg : '',
    data: data?.data && typeof data.data === 'object' ? data.data : null
  };
}

async function request(path, options = {}, timeoutMs) {
  // Optional client-side timeout so a request can never hang forever when the
  // backend is slow or unreachable. Aborts the fetch after timeoutMs and throws
  // a clear error the caller can treat as a failure.
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
    if (error && error.name === 'AbortError') {
      throw new Error(`Request to ${path} timed out after ${timeoutMs}ms`);
    }
    throw error;
  } finally {
    if (timer) {
      clearTimeout(timer);
    }
  }

  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.message || envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope;
}

export async function login({ email, password }) {
  return request('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify({
      email,
      password
    })
  });
}

export async function restoreSession() {
  // Bounded timeout: this runs on first paint, so a hung backend must not leave
  // the UI stuck restoring. On timeout the auth store falls back to the
  // unauthenticated view and /api/startup/status polling corrects it.
  return request('/api/auth/session', { method: 'GET' }, 5000);
}

export async function logout() {
  return request('/api/auth/logout', {
    method: 'POST',
    body: JSON.stringify({})
  });
}

// 扫码登录：初始化，换取 device_code + 二维码内容(qr_payload)。
export async function scanInit() {
  return request('/api/auth/scan/init', {
    method: 'POST',
    body: JSON.stringify({})
  });
}

// 扫码登录：authorized 时两个 token 字段都是本地 session 凭证。
export async function scanStatus(deviceCode) {
  const query = new URLSearchParams({ device_code: deviceCode }).toString();
  return request(`/api/auth/scan/status?${query}`, {
    method: 'GET'
  });
}

// 扫码登录：用本地 session 兼容字段完成激活，返回与密码登录同构的用户信息。
export async function activateScanLogin({ sessionToken, refreshToken }) {
  return request('/api/auth/scan/activate', {
    method: 'POST',
    body: JSON.stringify({
      session_token: sessionToken,
      refresh_token: refreshToken
    })
  });
}
