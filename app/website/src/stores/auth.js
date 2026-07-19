import { computed, reactive, readonly, toRefs } from 'vue';
import {
  login as loginRequest,
  logout as logoutRequest,
  restoreSession as restoreSessionRequest,
  activateScanLogin as activateScanLoginRequest
} from '../services/authApi';
import { useI18n } from '../i18n';

const state = reactive({
  user: null,
  status: 'idle',
  isAuthenticated: false,
  isReady: false,
  loginPending: false,
  logoutPending: false,
  restorePending: false,
  loginError: '',
  restoreError: '',
  lastActionMessage: ''
});

// Incremented whenever authentication is accepted or invalidated. Async login
// and restore responses capture the current epoch so an older response cannot
// resurrect the UI after a HardInvalid/logout event has already cleared it.
let sessionEpoch = 0;

function normalizeUser(user) {
  if (!user || typeof user !== 'object') {
    return null;
  }

  return {
    id: Number(user.id || 0),
    username: typeof user.username === 'string' ? user.username : '',
    email: typeof user.email === 'string' ? user.email : '',
    role: typeof user.role === 'string' ? user.role : '',
    status: typeof user.status === 'string' ? user.status : '',
    balance: Number(user.balance || 0),
    concurrency: Number(user.concurrency || 0),
    allowedGroups: Array.isArray(user.allowed_groups)
      ? user.allowed_groups.map((value) => Number(value)).filter((value) => Number.isFinite(value))
      : [],
    createdAt: typeof user.created_at === 'string' ? user.created_at : '',
    profileUpdatedAt: typeof user.profile_updated_at === 'string' ? user.profile_updated_at : '',
    expiresIn: Number(user.expires_in || 0),
    expiresAt: typeof user.expires_at === 'string' ? user.expires_at : '',
    updatedAt: typeof user.updated_at === 'string' ? user.updated_at : ''
  };
}

function applyAuthenticatedState(user, message = '') {
  sessionEpoch += 1;
  state.user = normalizeUser(user);
  state.isAuthenticated = Boolean(state.user);
  state.status = state.isAuthenticated ? 'authenticated' : 'unauthenticated';
  state.loginError = '';
  state.restoreError = '';
  state.lastActionMessage = message;
}

function applyUnauthenticatedState(message = '', options = {}) {
  sessionEpoch += 1;
  state.user = null;
  state.isAuthenticated = false;
  state.status = 'unauthenticated';
  state.loginError = options.preserveLoginError ? state.loginError : '';
  state.restoreError = message;
  state.lastActionMessage = message;
}

export function syncUnauthenticatedAuthState(message = '', options = {}) {
  const { t } = useI18n();
  applyUnauthenticatedState(message || t('auth_pleaseLogin'), options);
}

// syncAuthFromStartupStatus syncs the auth store with the kernel state from
// the /api/startup/status response.  Called by useRunStatus every 5 seconds.
export function syncAuthFromStartupStatus(data) {
  if (!data || typeof data !== 'object') {
    return;
  }

  const user = data.user;
  const fetchSuccess = data.fetch_success;

  // Startup polling is a logout/reconciliation backstop, not a login source.
  // Only an explicit login/restore or Active SSE transition may authenticate a
  // previously unauthenticated UI. This blocks a stale in-flight READY response
  // from resurrecting the user after HardInvalid.
  if (state.isAuthenticated && user && typeof user === 'object' && fetchSuccess) {
    const normalized = normalizeUser(user);
    if (normalized) {
      state.user = normalized;
      state.isAuthenticated = true;
      state.status = 'authenticated';
      state.restoreError = '';
      return;
    }
  }

  if (!fetchSuccess && state.isAuthenticated) {
    const { t } = useI18n();
    applyUnauthenticatedState(t('auth_pleaseLogin'));
  }
}

export function mergeAuthUser(partialUser, message = '') {
  if (!state.user || !partialUser || typeof partialUser !== 'object') {
    return;
  }

  state.user = {
    ...state.user,
    ...partialUser
  };

  if (message) {
    state.lastActionMessage = message;
  }
}

export async function restoreAuthSession() {
  if (state.restorePending) {
    return state.isAuthenticated;
  }

  const { t } = useI18n();

  state.restorePending = true;
  state.isReady = false;
  state.status = 'restoring';
  state.restoreError = '';
  state.lastActionMessage = '';
  const requestEpoch = sessionEpoch;

  try {
    const result = await restoreSessionRequest();
    if (requestEpoch !== sessionEpoch) {
      return state.isAuthenticated;
    }
    if (result.status === 'success' && result.data) {
      applyAuthenticatedState(result.data, result.message || t('auth_sessionRestored'));
      return true;
    }

    applyUnauthenticatedState(result.message || t('auth_pleaseLogin'));
    return false;
  } catch (error) {
    applyUnauthenticatedState(error instanceof Error ? error.message : t('auth_pleaseLogin'));
    return false;
  } finally {
    state.restorePending = false;
    state.isReady = true;
  }
}

export async function loginWithPassword(credentials) {
  const { t } = useI18n();

  state.loginPending = true;
  state.loginError = '';
  state.lastActionMessage = '';
  const requestEpoch = sessionEpoch;

  try {
    const result = await loginRequest(credentials);
    if (requestEpoch !== sessionEpoch) {
      return state.isAuthenticated;
    }
    if (result.status !== 'success' || !result.data) {
      throw new Error(result.message || t('auth_loginFailed'));
    }

    applyAuthenticatedState(result.data, result.message || t('auth_loginSuccess'));
    state.isReady = true;
    return true;
  } catch (error) {
    if (requestEpoch !== sessionEpoch) {
      return false;
    }
    sessionEpoch += 1;
    state.user = null;
    state.isAuthenticated = false;
    state.status = 'unauthenticated';
    state.loginError = error instanceof Error ? error.message : t('auth_loginFailed');
    state.lastActionMessage = '';
    state.isReady = true;
    return false;
  } finally {
    state.loginPending = false;
  }
}

export async function completeScanLogin({ sessionToken, refreshToken }) {
  const { t } = useI18n();

  state.loginPending = true;
  state.loginError = '';
  state.lastActionMessage = '';
  const requestEpoch = sessionEpoch;

  try {
    const result = await activateScanLoginRequest({ sessionToken, refreshToken });
    if (requestEpoch !== sessionEpoch) {
      return state.isAuthenticated;
    }
    if (result.status !== 'success' || !result.data) {
      throw new Error(result.message || t('auth_loginFailed'));
    }

    applyAuthenticatedState(result.data, result.message || t('auth_loginSuccess'));
    state.isReady = true;
    return true;
  } catch (error) {
    if (requestEpoch !== sessionEpoch) {
      return false;
    }
    sessionEpoch += 1;
    state.user = null;
    state.isAuthenticated = false;
    state.status = 'unauthenticated';
    state.loginError = error instanceof Error ? error.message : t('auth_loginFailed');
    state.lastActionMessage = '';
    state.isReady = true;
    return false;
  } finally {
    state.loginPending = false;
  }
}

export async function logoutUser() {
  if (state.logoutPending) {
    return;
  }

  const { t } = useI18n();

  state.logoutPending = true;

  try {
    const result = await logoutRequest();
    applyUnauthenticatedState(result.message || t('auth_loggedOut'));
  } catch (error) {
    applyUnauthenticatedState(error instanceof Error ? error.message : t('auth_loggedOutLocally'));
  } finally {
    state.logoutPending = false;
    state.isReady = true;
  }
}

let sessionEventSource = null;

// connectSessionEvents opens an SSE subscription to /api/session/events so the
// dashboard reflects identity transitions (login / refresh / soft-expired /
// hard-invalid / logout) instantly, instead of waiting for the 5s
// /api/startup/status poll. EventSource auto-reconnects; on reconnect the server
// re-sends a state snapshot so the UI re-syncs.
export function connectSessionEvents() {
  if (sessionEventSource) return;
  if (typeof window === 'undefined' || typeof window.EventSource === 'undefined') return;

  sessionEventSource = new EventSource('/api/session/events');
  sessionEventSource.onmessage = (event) => {
    let payload;
    try {
      payload = JSON.parse(event.data);
    } catch (_) {
      return;
    }
    handleSessionPayload(payload);
  };
}

function handleSessionPayload(payload) {
  const { t } = useI18n();
  const target = payload.type === 'snapshot' ? payload.state : payload.to;

  if (target === 'active') {
    // (Re)fetch canonical user info; restoreAuthSession is idempotent
    // (guarded by restorePending), so the startup snapshot won't double-fetch.
    void restoreAuthSession();
  } else if (target === 'soft_expired') {
    // Degraded but recovering — keep the user visible, surface a notice.
    state.isReady = true;
    state.status = 'soft_expired';
    state.lastActionMessage = t('auth_restoringSession');
  } else {
    // hard_invalid / unauthenticated → show the login view.
    applyUnauthenticatedState(t('auth_pleaseLogin'));
  }
}

export function useAuthStore() {
  const { t } = useI18n();

  const userDisplayName = computed(() => {
    if (state.user?.username) {
      return state.user.username;
    }
    return t('auth_guest');
  });

  const planLabel = computed(() => {
    if (state.user?.status) {
      return state.user.status;
    }
    if (state.user?.email) {
      return state.user.email;
    }
    return t('auth_loginRequired');
  });

  const authNotice = computed(() => {
    if (state.isAuthenticated) {
      return state.lastActionMessage || t('auth_sessionActive');
    }
    if (state.restorePending) {
      return t('auth_restoringSession');
    }
    return state.loginError || state.restoreError || t('auth_loginPrompt');
  });

  return {
    ...toRefs(readonly(state)),
    userDisplayName,
    planLabel,
    authNotice,
    restoreAuthSession,
    loginWithPassword,
    completeScanLogin,
    logoutUser,
    mergeAuthUser
  };
}
