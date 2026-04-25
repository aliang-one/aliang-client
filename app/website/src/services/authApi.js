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
