import { ref, readonly } from 'vue';
import en from './en';
import zh from './zh';

const messages = { en, zh };
const STORAGE_KEY = 'aliang-lang';
const locale = ref(loadLocale());

function loadLocale() {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved && messages[saved]) return saved;
  return (navigator.language || 'en').startsWith('zh') ? 'zh' : 'en';
}

export function useI18n() {
  function t(key, params = {}) {
    const msg = messages[locale.value]?.[key] ?? messages.en?.[key] ?? key;
    if (!Object.keys(params).length) return msg;
    return msg.replace(/\{(\w+)\}/g, (_, k) => params[k] ?? `{${k}}`);
  }
  function setLocale(lang) {
    if (messages[lang]) {
      locale.value = lang;
      localStorage.setItem(STORAGE_KEY, lang);
    }
  }
  return { locale: readonly(locale), t, setLocale };
}
