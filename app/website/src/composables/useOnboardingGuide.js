import { ref } from 'vue';

const STORAGE_KEY = 'alianggate.onboarding.v1';

export const isOpen = ref(false);
export const currentStepIndex = ref(0);

export function isOnboardingCompleted() {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'completed';
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
  } catch (_) {
    // Ignore storage failures in restricted environments.
  }
}

export function openOnboardingGuide(stepIndex = 0) {
  currentStepIndex.value = Math.max(0, stepIndex);
  isOpen.value = true;
}

export function closeOnboardingGuide() {
  isOpen.value = false;
}

export function useOnboardingGuide() {
  return {
    isOpen,
    currentStepIndex,
    openOnboardingGuide,
    closeOnboardingGuide,
    markOnboardingCompleted,
    resetOnboardingProgress,
    isOnboardingCompleted
  };
}
