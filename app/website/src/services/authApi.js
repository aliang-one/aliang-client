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

async function request(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {})
    }
  });

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
  return request('/api/auth/session', {
    method: 'GET'
  });
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

// 扫码登录：按 device_code 轮询状态。authorized 时 data 含 session_token + refresh_token。
export async function scanStatus(deviceCode) {
  const query = new URLSearchParams({ device_code: deviceCode }).toString();
  return request(`/api/auth/scan/status?${query}`, {
    method: 'GET'
  });
}

// 扫码登录：用 st_(session_token) + refresh_token 完成本地激活，返回与密码登录同构的用户信息。
export async function activateScanLogin({ sessionToken, refreshToken }) {
  return request('/api/auth/scan/activate', {
    method: 'POST',
    body: JSON.stringify({
      session_token: sessionToken,
      refresh_token: refreshToken
    })
  });
}
