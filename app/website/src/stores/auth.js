import { computed, reactive, readonly, toRefs } from 'vue';
import { getCurrentUser as getCurrentUserRequest, login as loginRequest, logout as logoutRequest, restoreSession as restoreSessionRequest } from '../services/authApi';
import { useI18n } from '../i18n';

const authSessionPollIntervalMs = 60 * 1000;
const authRefreshLeadTimeMs = 10 * 60 * 1000;
let authSessionPollTimer = null;

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
  state.user = normalizeUser(user);
  state.isAuthenticated = Boolean(state.user);
  state.status = state.isAuthenticated ? 'authenticated' : 'unauthenticated';
  state.loginError = '';
  state.restoreError = '';
  state.lastActionMessage = message;
}

function refreshAuthenticatedUser(user) {
  const normalized = normalizeUser(user);
  if (!normalized) {
    return;
  }

  state.user = normalized;
  state.isAuthenticated = true;
  state.status = 'authenticated';
  state.restoreError = '';
}

function applyUnauthenticatedState(message = '', options = {}) {
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
  stopAuthSessionMonitor();
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

  try {
    const result = await restoreSessionRequest();
    if (result.status === 'success' && result.data) {
      applyAuthenticatedState(result.data, result.message || t('auth_sessionRestored'));
      startAuthSessionMonitor();
      return true;
    }

    applyUnauthenticatedState(result.message || t('auth_pleaseLogin'));
    stopAuthSessionMonitor();
    return false;
  } catch (error) {
    applyUnauthenticatedState(error instanceof Error ? error.message : t('auth_pleaseLogin'));
    stopAuthSessionMonitor();
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

  try {
    const result = await loginRequest(credentials);
    if (result.status !== 'success' || !result.data) {
      throw new Error(result.message || t('auth_loginFailed'));
    }

    applyAuthenticatedState(result.data, result.message || t('auth_loginSuccess'));
    startAuthSessionMonitor();
    state.isReady = true;
    return true;
  } catch (error) {
    state.user = null;
    state.isAuthenticated = false;
    state.status = 'unauthenticated';
    state.loginError = error instanceof Error ? error.message : t('auth_loginFailed');
    state.lastActionMessage = '';
    state.isReady = true;
    stopAuthSessionMonitor();
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
    stopAuthSessionMonitor();
    state.logoutPending = false;
    state.isReady = true;
  }
}

async function pollAuthSession() {
  if (state.restorePending || state.loginPending || state.logoutPending) {
    return;
  }

  const { t } = useI18n();
  const localExpiry = getLocalSessionExpiry(state.user);
  if (localExpiry !== null) {
    const remainingMs = localExpiry - Date.now();
    if (remainingMs <= 0) {
      syncUnauthenticatedAuthState(t('user_sessionExpired'));
      return;
    }
    if (remainingMs <= authRefreshLeadTimeMs) {
      return;
    }
  }

  try {
    const result = await getCurrentUserRequest();
    if (result.status === 'success' && result.data) {
      refreshAuthenticatedUser(result.data);
      return;
    }

    syncUnauthenticatedAuthState(result.message || t('auth_pleaseLogin'));
  } catch (error) {
    syncUnauthenticatedAuthState(error instanceof Error ? error.message : t('auth_pleaseLogin'));
  }
}

function startAuthSessionMonitor() {
  if (authSessionPollTimer !== null || !state.isAuthenticated) {
    return;
  }

  authSessionPollTimer = window.setInterval(() => {
    void pollAuthSession();
  }, authSessionPollIntervalMs);
}

function stopAuthSessionMonitor() {
  if (authSessionPollTimer === null) {
    return;
  }

  window.clearInterval(authSessionPollTimer);
  authSessionPollTimer = null;
}

function getLocalSessionExpiry(user) {
  if (!user || typeof user !== 'object') {
    return null;
  }

  const explicitExpiry = Date.parse(user.expiresAt || '');
  if (Number.isFinite(explicitExpiry)) {
    return explicitExpiry;
  }

  const updatedAt = Date.parse(user.updatedAt || '');
  if (!Number.isFinite(updatedAt)) {
    return null;
  }

  const expiresInMs = Number(user.expiresIn || 0) * 1000;
  if (!Number.isFinite(expiresInMs) || expiresInMs <= 0) {
    return null;
  }

  return updatedAt + expiresInMs;
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
    logoutUser,
    mergeAuthUser
  };
}
