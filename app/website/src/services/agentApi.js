function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    data: typeof payload.data !== 'undefined' ? payload.data : null,
  };
}

async function request(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  });

  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope.data;
}

export async function getAgentStatus() {
  return request('/api/agent/status', { method: 'GET' });
}

export async function startAgentBinding() {
  return request('/api/agent/bind/start', { method: 'POST' });
}

export async function getAgentBindingStatus(sessionId) {
  const query = new URLSearchParams({ session_id: sessionId });
  return request(`/api/agent/bind/status?${query.toString()}`, { method: 'GET' });
}

export async function disableAgent() {
  return request('/api/agent/disable', { method: 'POST' });
}

export async function launchAgentTool(payload) {
  return request('/api/agent/tools/launch', {
    method: 'POST',
    body: JSON.stringify(payload || {}),
  });
}
