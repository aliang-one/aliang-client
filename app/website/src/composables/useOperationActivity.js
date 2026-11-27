import { computed, onMounted, onUnmounted, shallowRef, ref } from 'vue';
import { assistPrefix } from '../services/assistPrefix';
import { isActiveOperation, mergeOperation } from '../utils/operationActivity';

export function useOperationActivity() {
  const rows = shallowRef(new Map());
  const connected = ref(false);
  const syncedAt = ref('');
  let source;

  onMounted(() => {
    if (typeof EventSource === 'undefined') return;
    // SSE 走 HTTP,同构代理镜像挂载(/assist/service/)下过前缀即可送达。
    source = new EventSource(assistPrefix() + '/api/agent/operations/events');
    source.onmessage = (event) => {
      let snapshot;
      try { snapshot = JSON.parse(event.data); } catch { return; }
      if (!Array.isArray(snapshot?.operations)) return;
      const next = new Map();
      for (const operation of snapshot.operations) {
        const previous = rows.value.get(operation?.operation_id);
        const merged = mergeOperation(previous ? new Map([[previous.operation_id, previous]]) : new Map(), operation);
        for (const [id, value] of merged) next.set(id, value);
      }
      rows.value = next;
      connected.value = snapshot.connected === true;
      syncedAt.value = typeof snapshot.synced_at === 'string' ? snapshot.synced_at : '';
    };
    source.onerror = () => { connected.value = false; };
  });
  onUnmounted(() => source?.close());

  const operations = computed(() => [...rows.value.values()]);
  const activeCount = computed(() => operations.value.filter(isActiveOperation).length);
  return { operations, activeCount, connected, syncedAt };
}
