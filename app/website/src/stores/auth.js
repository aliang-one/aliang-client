import { computed, reactive, readonly, toRefs } from 'vue';
import {
  AuthRequestError,
  activateScanLogin as activateScanLoginRequest,
  bootstrapDashboardSession,
  login as loginRequest,
  logout as logoutRequest,
  restoreSession as restoreSessionRequest
} from '../services/authApi';
import { useI18n } from '../i18n';
import {
  createSessionViewState,
  normalizeSessionUser,
  reduceSessionReadFailure,
  reduceSessionSnapshot
} from './sessionSnapshot';

const initialSession = createSessionViewState();
const state = reactive({
  user: initialSession.user,
  status: initialSession.status,
  sessionState: initialSession.sessionState,
  sessionInstanceId: initialSession.instanceId,
  sessionRevision: initialSession.revision,
  retiredSessionInstanceIds: initialSession.retiredInstanceIds,
  isAuthenticated: initialSession.isAuthenticated,
  isReady: false,
  loginPending: false,
  logoutPending: false,
  restorePending: false,
  loginError: '',
  restoreError: '',
  lastActionMessage: '',
  connectionStatus: 'connecting'
});

function currentSessionView() {
  return createSessionViewState({
    instanceId: state.sessionInstanceId,
    revision: state.sessionRevision,
    retiredInstanceIds: [...state.retiredSessionInstanceIds],
    sessionState: state.sessionState,
    user: state.user,
    isAuthenticated: state.isAuthenticated,
    status: state.status
  });
}

function commitSessionView(next) {
  state.sessionInstanceId = next.instanceId;
  state.sessionRevision = next.revision;
  state.retiredSessionInstanceIds = [...next.retiredInstanceIds];
  state.sessionState = next.sessionState;
  state.user = next.user;
  state.isAuthenticated = next.isAuthenticated;
  state.status = next.status;
}

export function applySessionSnapshot(payload, message = '') {
  const result = reduceSessionSnapshot(currentSessionView(), payload);
  if (!result.accepted) return false;

  commitSessionView(result.state);
  state.isReady = true;
  state.restoreError = '';
  state.connectionStatus = 'connected';
  if (result.state.sessionState === 'active') {
    state.loginError = '';
  }
  if (message) state.lastActionMessage = message;
  return true;
}

function applySessionReadFailure(error) {
  const errorType = error instanceof AuthRequestError ? error.type : 'server_error';
  if (error instanceof AuthRequestError && error.status === 401) {
    dashboardSessionReady = false;
  }
  commitSessionView(reduceSessionReadFailure(currentSessionView(), errorType));
  state.connectionStatus = 'disconnected';
  state.restoreError = error instanceof Error ? error.message : 'Session state is unavailable';
  state.isReady = true;
}

// Auth-like failures from feature APIs are signals to reconcile with the
// authority. They are never themselves authoritative logout events.
export function syncUnauthenticatedAuthState(message = '') {
  if (message) state.restoreError = message;
  void restoreAuthSession({ background: true });
}

export function syncAuthFromStartupStatus(data) {
  if (data?.session) applySessionSnapshot(data.session);
}

export function mergeAuthUser(partialUser, message = '') {
  if (!state.user || !partialUser || typeof partialUser !== 'object') return;
  state.user = { ...state.user, ...partialUser };
  if (message) state.lastActionMessage = message;
}

let reconcileSequence = 0;

export async function restoreAuthSession(options = {}) {
  const { background = false, force = false } = options;
  if (state.restorePending && !force) return state.isAuthenticated;
  const { t } = useI18n();
  const requestSequence = ++reconcileSequence;
  state.restorePending = true;
  if (!background && !state.sessionInstanceId) {
    state.status = 'restoring';
    state.lastActionMessage = '';
  }

  try {
    const snapshot = await restoreSessionRequest();
    if (requestSequence !== reconcileSequence) return state.isAuthenticated;
    applySessionSnapshot(snapshot,
      snapshot?.state === 'active' ? t('auth_sessionRestored') : '');
    return state.isAuthenticated;
  } catch (error) {
    if (requestSequence === reconcileSequence) applySessionReadFailure(error);
    return state.isAuthenticated;
  } finally {
    if (requestSequence === reconcileSequence) state.restorePending = false;
    state.isReady = true;
  }
}

let dashboardBootstrapPromise = null;
let dashboardSessionReady = false;

async function ensureDashboardSession() {
  if (dashboardSessionReady) return;
  if (!dashboardBootstrapPromise) {
    dashboardBootstrapPromise = bootstrapDashboardSession()
      .then(() => { dashboardSessionReady = true; })
      .finally(() => { dashboardBootstrapPromise = null; });
  }
  return dashboardBootstrapPromise;
}

async function reconcileAfterAuthCommand() {
  await ensureDashboardSession();
  connectSessionEvents();
  return restoreAuthSession({ force: true });
}

export async function loginWithPassword(credentials) {
  const { t } = useI18n();
  state.loginPending = true;
  state.loginError = '';
  state.lastActionMessage = '';
  try {
    const result = await loginRequest(credentials);
    if (result.status !== 'success') throw new Error(result.message || t('auth_loginFailed'));
    await reconcileAfterAuthCommand();
    if (!state.isAuthenticated) throw new Error(t('auth_loginFailed'));
    state.lastActionMessage = result.message || t('auth_loginSuccess');
    return true;
  } catch (error) {
    state.loginError = error instanceof Error ? error.message : t('auth_loginFailed');
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
  try {
    const result = await activateScanLoginRequest({ sessionToken, refreshToken });
    if (result.status !== 'success') throw new Error(result.message || t('auth_loginFailed'));
    await reconcileAfterAuthCommand();
    if (!state.isAuthenticated) throw new Error(t('auth_loginFailed'));
    state.lastActionMessage = result.message || t('auth_loginSuccess');
    return true;
  } catch (error) {
    state.loginError = error instanceof Error ? error.message : t('auth_loginFailed');
    state.isReady = true;
    return false;
  } finally {
    state.loginPending = false;
  }
}

export async function logoutUser() {
  if (state.logoutPending) return;
  const { t } = useI18n();
  state.logoutPending = true;
  try {
    const result = await logoutRequest();
    await reconcileAfterAuthCommand();
    state.lastActionMessage = result.message || t('auth_loggedOut');
  } catch (error) {
    // Preserve the last authoritative user on transport/server failure. SSE or
    // the periodic pure GET will converge if the backend completed the logout.
    applySessionReadFailure(error);
    state.lastActionMessage = '';
  } finally {
    state.logoutPending = false;
    state.isReady = true;
  }
}

let sessionEventSource = null;
let sessionEventSourceGeneration = 0;

export function connectSessionEvents() {
  if (sessionEventSource) return;
  if (typeof window === 'undefined' || typeof window.EventSource === 'undefined') return;

  const generation = ++sessionEventSourceGeneration;
  const source = new window.EventSource('/api/session/events');
  sessionEventSource = source;
  source.onopen = () => {
    if (generation === sessionEventSourceGeneration) state.connectionStatus = 'connected';
  };
  source.onmessage = (event) => {
    if (generation !== sessionEventSourceGeneration) return;
    try {
      applySessionSnapshot(JSON.parse(event.data));
    } catch (_) {
      // Ignore malformed events; the periodic GET remains authoritative.
    }
  };
  source.onerror = () => {
    if (generation !== sessionEventSourceGeneration) return;
    state.connectionStatus = 'disconnected';
    void restoreAuthSession({ background: true });
  };
}

let reconciliationTimer = null;

function startPeriodicReconciliation() {
  if (reconciliationTimer || typeof window === 'undefined') return;
  reconciliationTimer = window.setInterval(() => {
    void ensureDashboardSession()
      .then(() => {
        connectSessionEvents();
        return restoreAuthSession({ background: true });
      })
      .catch(applySessionReadFailure);
  }, 5000);
}

export async function initializeAuthSession() {
  state.status = 'restoring';
  state.isReady = false;
  startPeriodicReconciliation();
  try {
    await ensureDashboardSession();
    connectSessionEvents();
    return await restoreAuthSession();
  } catch (error) {
    applySessionReadFailure(error);
    return state.isAuthenticated;
  }
}

export function useAuthStore() {
  const { t } = useI18n();
  const userDisplayName = computed(() => state.user?.username || t('auth_guest'));
  const planLabel = computed(() => state.user?.status || state.user?.email || t('auth_loginRequired'));
  const authNotice = computed(() => {
    if (state.status === 'transport_unavailable' || state.status === 'server_error') {
      return state.restoreError || t('auth_restoringSession');
    }
    if (state.sessionState === 'soft_expired' || state.sessionState === 'restoring' || state.restorePending) {
      return t('auth_restoringSession');
    }
    if (state.isAuthenticated) return state.lastActionMessage || t('auth_sessionActive');
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

export { normalizeSessionUser };
