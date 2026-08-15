import { ref, readonly } from 'vue';

const STORAGE_KEY = 'aliang-theme-mode';
const THEME_SYSTEM = 'system';
const THEME_LIGHT = 'light';
const THEME_DARK = 'dark';

const themeMode = ref(loadThemeMode());
const resolvedTheme = ref(resolveTheme(themeMode.value));

let mediaQuery = null;
let mediaQueryListenerAttached = false;

function isBrowser() {
  return typeof window !== 'undefined' && typeof document !== 'undefined';
}

function normalizeThemeMode(value) {
  if (value === THEME_LIGHT || value === THEME_DARK || value === THEME_SYSTEM) {
    return value;
  }
  return THEME_SYSTEM;
}

function readStorage(key) {
  if (!isBrowser()) {
    return null;
  }

  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key, value) {
  if (!isBrowser()) {
    return;
  }

  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Ignore storage write failures and keep the in-memory setting.
  }
}

function getSystemTheme() {
  if (!isBrowser() || typeof window.matchMedia !== 'function') {
    return THEME_LIGHT;
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? THEME_DARK : THEME_LIGHT;
}

function resolveTheme(mode) {
  const normalizedMode = normalizeThemeMode(mode);
  return normalizedMode === THEME_SYSTEM ? getSystemTheme() : normalizedMode;
}

function applyTheme(mode = themeMode.value) {
  if (!isBrowser()) {
    return resolveTheme(mode);
  }

  const nextResolvedTheme = resolveTheme(mode);
  resolvedTheme.value = nextResolvedTheme;

  document.documentElement.classList.toggle('dark', nextResolvedTheme === THEME_DARK);
  document.documentElement.dataset.themeMode = normalizeThemeMode(mode);
  document.documentElement.dataset.themeResolved = nextResolvedTheme;
  document.documentElement.style.colorScheme = nextResolvedTheme;

  return nextResolvedTheme;
}

function handleSystemThemeChange() {
  if (themeMode.value === THEME_SYSTEM) {
    applyTheme(THEME_SYSTEM);
  }
}

function ensureSystemThemeListener() {
  if (!isBrowser() || typeof window.matchMedia !== 'function') {
    return;
  }

  if (!mediaQuery) {
    mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
  }

  if (mediaQueryListenerAttached) {
    return;
  }

  if (typeof mediaQuery.addEventListener === 'function') {
    mediaQuery.addEventListener('change', handleSystemThemeChange);
  } else if (typeof mediaQuery.addListener === 'function') {
    mediaQuery.addListener(handleSystemThemeChange);
  }

  mediaQueryListenerAttached = true;
}

function loadThemeMode() {
  return normalizeThemeMode(readStorage(STORAGE_KEY));
}

export function initializeTheme() {
  themeMode.value = loadThemeMode();
  ensureSystemThemeListener();
  applyTheme(themeMode.value);
  return themeMode.value;
}

export function setThemeMode(mode) {
  const normalizedMode = normalizeThemeMode(mode);
  themeMode.value = normalizedMode;
  writeStorage(STORAGE_KEY, normalizedMode);
  applyTheme(normalizedMode);
}

export function useTheme() {
  return {
    themeMode: readonly(themeMode),
    resolvedTheme: readonly(resolvedTheme),
    setThemeMode,
    initializeTheme
  };
}

