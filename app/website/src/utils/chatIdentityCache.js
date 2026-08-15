export const CHAT_IDENTITY_STORAGE_KEY = 'aliang.user-center.profile.v1';
export const CHAT_IDENTITY_EVENT_NAME = 'aliang:chat-identity-updated';

const CHAT_IDENTITY_SOURCE = '/api/user-center/profile';
const CHAT_IDENTITY_TRUST_LEVEL = 'profile_api_verified';
const CHAT_IDENTITY_VERSION = 1;

function canUseStorage() {
  return typeof window !== 'undefined' && typeof window.localStorage !== 'undefined';
}

function normalizeString(value) {
  return typeof value === 'string' ? value : '';
}

export function normalizeChatIdentityProfile(profile, options = {}) {
  const raw = profile && typeof profile === 'object' ? profile : {};
  const savedAt = Number(options.savedAt || Date.now());

  return {
    id: Number(raw.id || 0),
    email: normalizeString(raw.email),
    username: normalizeString(raw.username),
    role: normalizeString(raw.role),
    status: normalizeString(raw.status),
    account_status: normalizeString(raw.status),
    updated_at: normalizeString(raw.updated_at),
    source: normalizeString(options.source) || CHAT_IDENTITY_SOURCE,
    trust_level: normalizeString(options.trustLevel) || CHAT_IDENTITY_TRUST_LEVEL,
    saved_at: Number.isFinite(savedAt) ? savedAt : Date.now()
  };
}

function dispatchChatIdentityEvent(profile) {
  if (typeof window === 'undefined' || typeof window.dispatchEvent !== 'function') {
    return;
  }

  try {
    window.dispatchEvent(new CustomEvent(CHAT_IDENTITY_EVENT_NAME, {
      detail: profile ? { profile } : { profile: null }
    }));
  } catch (_) {
    // Ignore event dispatch failures in restricted browser contexts.
  }
}

export function persistChatIdentityProfile(profile, options = {}) {
  if (!canUseStorage()) {
    return null;
  }

  const normalized = normalizeChatIdentityProfile(profile, options);
  const payload = {
    version: CHAT_IDENTITY_VERSION,
    saved_at: normalized.saved_at,
    profile: normalized
  };

  try {
    window.localStorage.setItem(CHAT_IDENTITY_STORAGE_KEY, JSON.stringify(payload));
    dispatchChatIdentityEvent(normalized);
    return normalized;
  } catch (_) {
    return null;
  }
}

export function readChatIdentityProfile() {
  if (!canUseStorage()) {
    return null;
  }

  try {
    const raw = window.localStorage.getItem(CHAT_IDENTITY_STORAGE_KEY);
    if (!raw) {
      return null;
    }

    const parsed = JSON.parse(raw);
    const profile = parsed && typeof parsed === 'object' && parsed.profile && typeof parsed.profile === 'object'
      ? parsed.profile
      : null;

    return profile ? normalizeChatIdentityProfile(profile, {
      source: profile.source,
      trustLevel: profile.trust_level,
      savedAt: profile.saved_at || parsed.saved_at
    }) : null;
  } catch (_) {
    return null;
  }
}

export function clearChatIdentityProfileCache() {
  if (!canUseStorage()) {
    return;
  }

  try {
    window.localStorage.removeItem(CHAT_IDENTITY_STORAGE_KEY);
  } catch (_) {
    return;
  }

  dispatchChatIdentityEvent(null);
}
