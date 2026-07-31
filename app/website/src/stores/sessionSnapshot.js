const SESSION_STATES = new Set([
  'restoring',
  'unauthenticated',
  'active',
  'soft_expired',
  'hard_invalid'
]);

export function normalizeSessionUser(user) {
  if (!user || typeof user !== 'object') return null;
  return {
    id: Number(user.id || 0),
    username: typeof user.username === 'string' ? user.username : '',
    email: typeof user.email === 'string' ? user.email : '',
    role: typeof user.role === 'string' ? user.role : '',
    status: typeof user.status === 'string' ? user.status : '',
    balance: Number(user.balance || 0),
    concurrency: Number(user.concurrency || 0),
    allowedGroups: Array.isArray(user.allowed_groups)
      ? user.allowed_groups.map(Number).filter(Number.isFinite)
      : [],
    createdAt: typeof user.created_at === 'string' ? user.created_at : '',
    profileUpdatedAt: typeof user.profile_updated_at === 'string' ? user.profile_updated_at : '',
    expiresIn: Number(user.expires_in || 0),
    expiresAt: typeof user.expires_at === 'string' ? user.expires_at : '',
    updatedAt: typeof user.updated_at === 'string' ? user.updated_at : ''
  };
}

export function createSessionViewState(overrides = {}) {
  return {
    instanceId: '',
    revision: 0,
    retiredInstanceIds: [],
    sessionState: 'restoring',
    user: null,
    isAuthenticated: false,
    status: 'idle',
    ...overrides
  };
}

function normalizeSnapshot(payload) {
  if (!payload || typeof payload !== 'object') return null;
  if (payload.type !== 'session_snapshot') return null;
  const instanceId = typeof payload.instance_id === 'string' ? payload.instance_id.trim() : '';
  const revision = Number(payload.revision);
  const sessionState = typeof payload.state === 'string' ? payload.state.trim().toLowerCase() : '';
  if (!instanceId || !Number.isSafeInteger(revision) || revision < 1 || !SESSION_STATES.has(sessionState)) {
    return null;
  }
  return { instanceId, revision, sessionState, user: normalizeSessionUser(payload.user) };
}

export function reduceSessionSnapshot(current, payload) {
  const snapshot = normalizeSnapshot(payload);
  if (!snapshot) return { accepted: false, state: current };

  const retired = new Set(current.retiredInstanceIds || []);
  if (current.instanceId === snapshot.instanceId) {
    if (snapshot.revision <= current.revision) return { accepted: false, state: current };
  } else {
    if (retired.has(snapshot.instanceId)) return { accepted: false, state: current };
    if (current.instanceId) retired.add(current.instanceId);
  }

  if (snapshot.sessionState === 'active' && !snapshot.user) {
    return { accepted: false, state: current };
  }

  let user = current.user;
  let isAuthenticated = current.isAuthenticated;
  let status = snapshot.sessionState;
  if (snapshot.sessionState === 'active') {
    user = snapshot.user;
    isAuthenticated = true;
    status = 'authenticated';
  } else if (snapshot.sessionState === 'soft_expired') {
    user = snapshot.user || current.user;
    isAuthenticated = Boolean(user);
  } else if (snapshot.sessionState === 'unauthenticated' || snapshot.sessionState === 'hard_invalid') {
    user = null;
    isAuthenticated = false;
    status = 'unauthenticated';
  }

  return {
    accepted: true,
    state: {
      ...current,
      instanceId: snapshot.instanceId,
      revision: snapshot.revision,
      retiredInstanceIds: Array.from(retired),
      sessionState: snapshot.sessionState,
      user,
      isAuthenticated,
      status
    }
  };
}

export function reduceSessionReadFailure(current, errorType) {
  const status = ['transport_unavailable', 'server_error'].includes(errorType)
    ? errorType
    : 'server_error';
  return { ...current, status };
}
