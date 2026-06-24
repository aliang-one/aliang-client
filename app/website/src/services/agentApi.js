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

export async function enableAgent() {
  return request('/api/agent/enable', { method: 'POST' });
}

export async function disableAgent(reason = 'manual') {
  return request('/api/agent/disable', {
    method: 'POST',
    body: JSON.stringify({ reason }),
  });
}

export async function launchAgentTool(payload) {
  return request('/api/agent/tools/launch', {
    method: 'POST',
    body: JSON.stringify(payload || {}),
  });
}

export async function getAgentSessions() {
  return request('/api/agent/sessions', { method: 'GET' });
}

export async function getAgentSessionDetail(sessionId, { limit, before } = {}) {
  const params = new URLSearchParams();
  if (limit) params.set('limit', String(limit));
  if (before) params.set('before', before);
  const query = params.toString();
  return request(
    `/api/agent/session?id=${encodeURIComponent(sessionId)}${query ? `&${query}` : ''}`,
    { method: 'GET' }
  );
}

export async function getAgentScanDirectories() {
  return request('/api/agent/scan-directories', { method: 'GET' });
}

export async function setAgentScanDirectories({ enabled, directories }) {
  return request('/api/agent/scan-directories', {
    method: 'POST',
    body: JSON.stringify({ enabled, directories }),
  });
}
