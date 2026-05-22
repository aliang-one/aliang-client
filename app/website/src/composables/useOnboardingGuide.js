import { ref } from 'vue';

const STORAGE_KEY_V1 = 'alianggate.onboarding.v1';
const STORAGE_KEY = 'alianggate.onboarding.v2';
const PROFILE_KEY = 'alianggate.onboarding.profile';

export const CLIENT_PROFILES = ['cli', 'ide', 'both'];

export const isOpen = ref(false);
export const currentStepIndex = ref(0);
export const clientProfile = ref(loadClientProfile());

function loadClientProfile() {
  try {
    const saved = localStorage.getItem(PROFILE_KEY);
    return CLIENT_PROFILES.includes(saved) ? saved : null;
  } catch (_) {
    return null;
  }
}

export function setClientProfile(profile) {
  if (!CLIENT_PROFILES.includes(profile)) {
    return;
  }
  clientProfile.value = profile;
  try {
    localStorage.setItem(PROFILE_KEY, profile);
  } catch (_) {
    // Ignore storage failures in restricted environments.
  }
}

export function isOnboardingCompleted() {
  try {
    return (
      localStorage.getItem(STORAGE_KEY) === 'completed' ||
      localStorage.getItem(STORAGE_KEY_V1) === 'completed'
    );
  } catch (_) {
    return false;
  }
}

export function markOnboardingCompleted() {
  try {
    localStorage.setItem(STORAGE_KEY, 'completed');
  } catch (_) {
    // Ignore storage failures in restricted environments.
  }
}

export function resetOnboardingProgress() {
  try {
    localStorage.removeItem(STORAGE_KEY);
    localStorage.removeItem(STORAGE_KEY_V1);
    localStorage.removeItem(PROFILE_KEY);
  } catch (_) {
    // Ignore storage failures in restricted environments.
  }
  clientProfile.value = null;
}

export function openOnboardingGuide(stepIndex) {
  const resolvedIndex = typeof stepIndex === 'number'
    ? stepIndex
    : (clientProfile.value ? 1 : 0);
  currentStepIndex.value = Math.max(0, resolvedIndex);
  isOpen.value = true;
}

export function closeOnboardingGuide() {
  isOpen.value = false;
}

export function useOnboardingGuide() {
  return {
    isOpen,
    currentStepIndex,
    clientProfile,
    openOnboardingGuide,
    closeOnboardingGuide,
    markOnboardingCompleted,
    resetOnboardingProgress,
    isOnboardingCompleted,
    setClientProfile
  };
}
