import { computed, ref } from 'vue';
import { dismissSoftwareUpdate, getSoftwareUpdateStatus } from '../services/softwareUpdateApi';

const softwareUpdateStatus = ref({
  software: '',
  platform: '',
  current_version: '',
  latest_version: '',
  download_url: '',
  file_type: '',
  changelog: '',
  needs_update: false,
  force_update: false,
  dismissed: false,
  show_modal: false,
  indicator_visible: false,
  blocking_proxy_start: false,
  status: 'unknown',
  last_error: ''
});
const softwareUpdateLoading = ref(false);
const softwareUpdateLoaded = ref(false);
const softwareUpdateError = ref('');
const softwareUpdateModalOpen = ref(false);
const softwareUpdateDismissPending = ref(false);

function normalizeStatus(payload = {}) {
  return {
    software: typeof payload.software === 'string' ? payload.software : '',
    platform: typeof payload.platform === 'string' ? payload.platform : '',
    current_version: typeof payload.current_version === 'string' ? payload.current_version : '',
    latest_version: typeof payload.latest_version === 'string' ? payload.latest_version : '',
    download_url: typeof payload.download_url === 'string' ? payload.download_url : '',
    file_type: typeof payload.file_type === 'string' ? payload.file_type : '',
    changelog: typeof payload.changelog === 'string' ? payload.changelog : '',
    needs_update: Boolean(payload.needs_update),
    force_update: Boolean(payload.force_update),
    dismissed: Boolean(payload.dismissed),
    show_modal: Boolean(payload.show_modal),
    indicator_visible: Boolean(payload.indicator_visible),
    blocking_proxy_start: Boolean(payload.blocking_proxy_start),
    status: typeof payload.status === 'string' ? payload.status : 'unknown',
    last_error: typeof payload.last_error === 'string' ? payload.last_error : '',
    checked_at_unix: Number(payload.checked_at_unix || 0),
    first_seen_at_unix: Number(payload.first_seen_at_unix || 0),
    last_seen_at_unix: Number(payload.last_seen_at_unix || 0),
    dismissed_at_unix: Number(payload.dismissed_at_unix || 0)
  };
}

function applySoftwareUpdateStatus(payload, { openModalIfNeeded = true } = {}) {
  const nextStatus = normalizeStatus(payload);
  softwareUpdateStatus.value = nextStatus;
  softwareUpdateError.value = nextStatus.last_error || '';
  softwareUpdateLoaded.value = true;

  if (!nextStatus.needs_update) {
    softwareUpdateModalOpen.value = false;
    return nextStatus;
  }

  if (openModalIfNeeded && nextStatus.show_modal) {
    softwareUpdateModalOpen.value = true;
  }

  return nextStatus;
}

async function refreshSoftwareUpdateStatus({ openModalIfNeeded = true } = {}) {
  softwareUpdateLoading.value = true;
  try {
    const envelope = await getSoftwareUpdateStatus();
    return applySoftwareUpdateStatus(envelope?.data, { openModalIfNeeded });
  } catch (error) {
    softwareUpdateError.value = error instanceof Error ? error.message : 'Failed to sync software update status';
    throw error;
  } finally {
    softwareUpdateLoading.value = false;
  }
}

function openSoftwareUpdateModal() {
  if (!softwareUpdateStatus.value.needs_update) {
    return;
  }
  softwareUpdateModalOpen.value = true;
}

async function closeSoftwareUpdateModal() {
  if (!softwareUpdateModalOpen.value) {
    return;
  }
  if (softwareUpdateStatus.value.force_update) {
    return;
  }

  if (!softwareUpdateStatus.value.needs_update || softwareUpdateStatus.value.dismissed) {
    softwareUpdateModalOpen.value = false;
    return;
  }

  softwareUpdateDismissPending.value = true;
  try {
    const envelope = await dismissSoftwareUpdate();
    applySoftwareUpdateStatus(envelope?.data, { openModalIfNeeded: false });
    softwareUpdateModalOpen.value = false;
  } catch (error) {
    softwareUpdateError.value = error instanceof Error ? error.message : 'Failed to dismiss update notice';
    throw error;
  } finally {
    softwareUpdateDismissPending.value = false;
  }
}

const hasSoftwareUpdate = computed(() => softwareUpdateStatus.value.needs_update);
const blockingSoftwareUpdate = computed(() => softwareUpdateStatus.value.blocking_proxy_start);

export function useSoftwareUpdate() {
  return {
    softwareUpdateStatus,
    softwareUpdateLoading,
    softwareUpdateLoaded,
    softwareUpdateError,
    softwareUpdateModalOpen,
    softwareUpdateDismissPending,
    hasSoftwareUpdate,
    blockingSoftwareUpdate,
    applySoftwareUpdateStatus,
    refreshSoftwareUpdateStatus,
    openSoftwareUpdateModal,
    closeSoftwareUpdateModal
  };
}
