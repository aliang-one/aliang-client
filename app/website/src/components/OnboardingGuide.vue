<template>
  <Teleport to="body">
    <div
      v-if="isOpen"
      class="fixed inset-0 z-[200]"
      role="dialog"
      aria-modal="true"
      :aria-label="t('guide_title')"
    >
      <div class="absolute inset-0 bg-slate-900/55" @click="handleSkip"></div>

      <div
        v-if="spotlightStyle && currentStep.target"
        class="pointer-events-none absolute rounded-xl border-2 border-primary/80 transition-all duration-200"
        :style="spotlightStyle"
      ></div>

      <div
        ref="panelRef"
        class="absolute z-10 w-[min(92vw,420px)] rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-700 dark:bg-slate-900"
        :style="panelStyle"
      >
        <div class="flex items-start justify-between gap-3">
          <div>
            <p class="text-[11px] font-bold uppercase tracking-[0.24em] text-primary">{{ t('guide_title') }}</p>
            <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-white">{{ currentStep.title }}</h3>
          </div>
          <button
            type="button"
            class="inline-flex size-8 shrink-0 items-center justify-center rounded-full text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            :aria-label="t('guide_close')"
            @click="handleSkip"
          >
            <span class="material-symbols-outlined text-[20px]">close</span>
          </button>
        </div>

        <p v-if="currentStep.id !== 'profile'" class="mt-3 text-sm leading-6 text-slate-600 dark:text-slate-300">
          {{ currentStep.description }}
        </p>

        <div v-if="currentStep.id === 'profile'" class="mt-4 grid gap-2">
          <p class="text-sm leading-6 text-slate-600 dark:text-slate-300">{{ t('guide_stepProfileDesc') }}</p>
          <button
            v-for="option in profileOptions"
            :key="option.id"
            type="button"
            class="rounded-xl border px-4 py-3 text-left transition"
            :class="clientProfile === option.id
              ? 'border-primary bg-primary/5 text-slate-900 dark:text-white'
              : 'border-slate-200 hover:border-primary/50 dark:border-slate-700'"
            @click="selectProfile(option.id)"
          >
            <p class="text-sm font-semibold">{{ option.title }}</p>
            <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ option.description }}</p>
          </button>
        </div>

        <div class="mt-4 flex flex-wrap gap-2">
          <span
            v-for="(step, index) in steps"
            :key="step.id"
            class="inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-[11px] font-semibold transition"
            :class="stepChipClass(index, step)"
          >
            <span
              v-if="stepCompletion[step.id]"
              class="material-symbols-outlined text-[14px]"
            >check_circle</span>
            <span>{{ index + 1 }}. {{ step.shortLabel }}</span>
          </span>
        </div>

        <div class="mt-5 flex flex-wrap items-center justify-between gap-3">
          <p class="text-xs text-slate-400">{{ t('guide_stepCounter', { current: currentStepIndex + 1, total: steps.length }) }}</p>
          <div class="flex flex-wrap gap-2">
            <button
              v-if="currentStepIndex > 0"
              type="button"
              class="inline-flex h-9 items-center justify-center rounded-lg border border-slate-200 px-3 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
              @click="goPrevious"
            >
              {{ t('guide_prev') }}
            </button>
            <button
              v-if="currentStep.actionLabel"
              type="button"
              class="inline-flex h-9 items-center justify-center rounded-lg bg-primary/10 px-3 text-xs font-semibold text-primary transition hover:bg-primary/20"
              @click="runCurrentAction"
            >
              {{ currentStep.actionLabel }}
            </button>
            <button
              type="button"
              class="inline-flex h-9 items-center justify-center rounded-lg bg-primary px-3 text-xs font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!canAdvance"
              @click="goNext"
            >
              {{ isLastStep ? t('guide_finish') : t('guide_next') }}
            </button>
          </div>
        </div>

        <button
          v-if="isLastStep"
          type="button"
          class="mt-3 text-xs font-semibold text-primary transition hover:text-primary/80"
          @click="openDocs"
        >
          {{ t('guide_openDocs') }}
        </button>

        <button
          type="button"
          class="mt-3 block text-xs font-semibold text-slate-400 transition hover:text-slate-600 dark:hover:text-slate-200"
          @click="handleSkip"
        >
          {{ t('guide_skip') }}
        </button>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { useI18n } from '../i18n';
import { useCertStatus } from '../composables/useCertStatus';
import { useAuthStore } from '../stores/auth';
import { useRunStatus } from '../composables/useRunStatus';
import {
  clientProfile,
  closeOnboardingGuide,
  currentStepIndex,
  isOpen,
  markOnboardingCompleted,
  setClientProfile
} from '../composables/useOnboardingGuide';

const { t } = useI18n();
const { certStatus } = useCertStatus();
const { isAuthenticated } = useAuthStore();
const { runIsRunning, runMode } = useRunStatus();

const emit = defineEmits(['open-cert', 'open-login', 'open-quick-setup', 'open-settings', 'open-docs']);

const panelRef = ref(null);
const spotlightStyle = ref(null);
const panelStyle = ref({
  top: '50%',
  left: '50%',
  transform: 'translate(-50%, -50%)'
});

let layoutFrame = null;

const profileOptions = computed(() => [
  {
    id: 'cli',
    title: t('guide_profileCliTitle'),
    description: t('guide_profileCliDesc')
  },
  {
    id: 'ide',
    title: t('guide_profileIdeTitle'),
    description: t('guide_profileIdeDesc')
  },
  {
    id: 'both',
    title: t('guide_profileBothTitle'),
    description: t('guide_profileBothDesc')
  }
]);

const steps = computed(() => {
  const profile = clientProfile.value;
  const sequence = [
    {
      id: 'profile',
      target: null,
      title: t('guide_stepProfileTitle'),
      shortLabel: t('guide_stepProfileShort'),
      description: t('guide_stepProfileDesc'),
      actionLabel: null
    },
    {
      id: 'cert',
      target: '[data-guide="cert"]',
      title: t('guide_stepCertTitle'),
      shortLabel: t('guide_stepCertShort'),
      description: t('guide_stepCertDesc'),
      actionLabel: t('guide_stepCertAction')
    },
    {
      id: 'login',
      target: '[data-guide="account"]',
      title: t('guide_stepLoginTitle'),
      shortLabel: t('guide_stepLoginShort'),
      description: t('guide_stepLoginDesc'),
      actionLabel: t('guide_stepLoginAction')
    }
  ];

  if (profile === 'cli' || profile === 'both') {
    sequence.push({
      id: 'quick-setup',
      target: '[data-guide="quick-setup"]',
      title: t('guide_stepQuickSetupTitle'),
      shortLabel: t('guide_stepQuickSetupShort'),
      description: t('guide_stepQuickSetupDesc'),
      actionLabel: t('guide_stepQuickSetupAction')
    });
  }

  if (profile) {
    sequence.push({
      id: 'run-mode',
      target: '[data-guide="run-mode"]',
      title: profile === 'ide' ? t('guide_stepRunModeIdeTitle') : t('guide_stepRunModeCliTitle'),
      shortLabel: t('guide_stepRunModeShort'),
      description: profile === 'ide'
        ? t('guide_stepRunModeIdeDesc')
        : profile === 'cli'
          ? t('guide_stepRunModeCliDesc')
          : t('guide_stepRunModeBothDesc'),
      actionLabel: t('guide_stepRunModeAction')
    });
  }

  sequence.push({
    id: 'proxy',
    target: '[data-guide="power"]',
    title: t('guide_stepProxyTitle'),
    shortLabel: t('guide_stepProxyShort'),
    description: profile === 'ide'
      ? t('guide_stepProxyIdeDesc')
      : profile === 'cli'
        ? t('guide_stepProxyCliDesc')
        : t('guide_stepProxyDesc'),
    actionLabel: t('guide_stepProxyAction')
  });

  return sequence;
});

const expectedRunMode = computed(() => {
  if (clientProfile.value === 'ide') {
    return 'tun';
  }
  if (clientProfile.value === 'cli') {
    return 'http';
  }
  return null;
});

const stepCompletion = computed(() => ({
  profile: Boolean(clientProfile.value),
  cert: Boolean(certStatus.value?.is_installed && certStatus.value?.is_trusted),
  login: isAuthenticated.value,
  'quick-setup': false,
  'run-mode': expectedRunMode.value ? runMode.value === expectedRunMode.value : false,
  proxy: runIsRunning.value
}));

const currentStep = computed(() => steps.value[currentStepIndex.value] || steps.value[0]);
const isLastStep = computed(() => currentStepIndex.value >= steps.value.length - 1);
const canAdvance = computed(() => currentStep.value.id !== 'profile' || Boolean(clientProfile.value));

function stepChipClass(index, step) {
  if (stepCompletion.value[step.id]) {
    return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300';
  }
  if (index === currentStepIndex.value) {
    return 'bg-primary/10 text-primary';
  }
  return 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400';
}

function selectProfile(profile) {
  setClientProfile(profile);
}

function runCurrentAction() {
  switch (currentStep.value.id) {
    case 'cert':
      emit('open-cert');
      break;
    case 'login':
      emit('open-login');
      break;
    case 'quick-setup':
      emit('open-quick-setup');
      break;
    case 'run-mode':
      emit('open-settings');
      break;
    case 'proxy':
      document.querySelector(currentStep.value.target)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
      break;
    default:
      break;
  }
}

function openDocs() {
  emit('open-docs', 'usage-guide');
}

function goPrevious() {
  if (currentStepIndex.value > 0) {
    currentStepIndex.value -= 1;
  }
}

function goNext() {
  if (!canAdvance.value) {
    return;
  }
  if (isLastStep.value) {
    finishGuide();
    return;
  }
  currentStepIndex.value += 1;
}

function handleSkip() {
  finishGuide();
}

function finishGuide() {
  markOnboardingCompleted();
  closeOnboardingGuide();
}

function scheduleLayout() {
  if (layoutFrame !== null) {
    window.cancelAnimationFrame(layoutFrame);
  }
  layoutFrame = window.requestAnimationFrame(() => {
    layoutFrame = null;
    updateLayout();
  });
}

function updateLayout() {
  if (!isOpen.value) {
    spotlightStyle.value = null;
    panelStyle.value = {
      top: '50%',
      left: '50%',
      transform: 'translate(-50%, -50%)'
    };
    return;
  }

  if (!currentStep.value.target) {
    spotlightStyle.value = null;
    panelStyle.value = {
      top: '50%',
      left: '50%',
      transform: 'translate(-50%, -50%)'
    };
    return;
  }

  const target = document.querySelector(currentStep.value.target);
  if (!target) {
    spotlightStyle.value = null;
    panelStyle.value = {
      top: '50%',
      left: '50%',
      transform: 'translate(-50%, -50%)'
    };
    return;
  }

  const rect = target.getBoundingClientRect();
  const padding = 8;
  const top = Math.max(8, rect.top - padding);
  const left = Math.max(8, rect.left - padding);
  const width = Math.min(window.innerWidth - 16, rect.width + padding * 2);
  const height = Math.min(window.innerHeight - 16, rect.height + padding * 2);

  spotlightStyle.value = {
    top: `${top}px`,
    left: `${left}px`,
    width: `${width}px`,
    height: `${height}px`,
    boxShadow: '0 0 0 9999px rgba(15, 23, 42, 0.62)'
  };

  const panelHeight = panelRef.value?.offsetHeight || 280;
  const panelWidth = panelRef.value?.offsetWidth || 420;
  const gap = 16;
  const preferBelow = top + height + gap + panelHeight <= window.innerHeight - 12;
  const preferRight = left + width + gap + panelWidth <= window.innerWidth - 12;

  let panelTop = preferBelow ? top + height + gap : Math.max(12, top - panelHeight - gap);
  let panelLeft = preferRight ? left + width + gap : Math.max(12, Math.min(left, window.innerWidth - panelWidth - 12));

  if (panelTop + panelHeight > window.innerHeight - 12) {
    panelTop = Math.max(12, window.innerHeight - panelHeight - 12);
  }

  panelStyle.value = {
    top: `${panelTop}px`,
    left: `${panelLeft}px`,
    transform: 'none'
  };
}

function onViewportChange() {
  if (isOpen.value) {
    scheduleLayout();
  }
}

watch(clientProfile, () => {
  if (currentStepIndex.value >= steps.value.length) {
    currentStepIndex.value = Math.max(0, steps.value.length - 1);
  }
});

watch(isOpen, async (open) => {
  if (!open) {
    spotlightStyle.value = null;
    return;
  }
  await nextTick();
  scheduleLayout();
});

watch(currentStepIndex, async () => {
  if (!isOpen.value) {
    return;
  }
  await nextTick();
  scheduleLayout();
});

onMounted(() => {
  window.addEventListener('resize', onViewportChange);
  window.addEventListener('scroll', onViewportChange, true);
});

onUnmounted(() => {
  window.removeEventListener('resize', onViewportChange);
  window.removeEventListener('scroll', onViewportChange, true);
  if (layoutFrame !== null) {
    window.cancelAnimationFrame(layoutFrame);
  }
});
</script>
