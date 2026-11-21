import { computed, ref } from 'vue';
import { getWebTerminalState, webTerminalCommand } from '../services/agentApi';

// 网页终端保险箱客户端状态机(模块级单例,同 useAgentStatus 形态)。
// 令牌只存内存(ref)——刷新即失,重新输 PIN;不落 localStorage(安全面)。

const token = ref('');
const status = ref(null);
const loading = ref(false);
const error = ref(null);

export function resetForTests() {
  token.value = '';
  status.value = null;
  loading.value = false;
  error.value = null;
}

export function useWebTerminal() {
  const unlocked = computed(() => !!token.value);

  // 失败不抛(Task 16 onMounted 里 void wt.refresh() 靠内部 catch 防
  // unhandled rejection):保旧 status 展示陈旧真值,错误进 error 面。
  async function refresh() {
    loading.value = true;
    try {
      status.value = await getWebTerminalState();
      error.value = null;
      return status.value;
    } catch (e) {
      error.value = e;
      return null;
    } finally {
      loading.value = false;
    }
  }

  async function command(payload) {
    loading.value = true;
    try {
      return await webTerminalCommand(payload, token.value);
    } finally {
      loading.value = false;
    }
  }

  async function setup(pin) {
    const res = await command({ action: 'setup', pin });
    if (res.ok) await refresh();
    return res;
  }

  async function verify(pin) {
    const res = await command({ action: 'verify', pin });
    if (res.ok && res.token) token.value = res.token;
    return res;
  }

  async function changePin(oldPin, newPin) {
    return command({ action: 'change', old_pin: oldPin, new_pin: newPin });
  }

  async function lock() {
    const res = await command({ action: 'lock' });
    token.value = '';
    await refresh();
    return res;
  }

  async function setEnabled(enabled) {
    const res = await command({ action: 'gates', enabled });
    if (!enabled) token.value = '';
    await refresh();
    return res;
  }

  async function killall() {
    return command({ action: 'killall' });
  }

  async function resetPin() {
    const res = await command({ action: 'reset' });
    token.value = '';
    await refresh();
    return res;
  }

  // 令牌失效(服务端 15 分钟闲置重锁杀终端断 WS):清本地令牌,工作台回落
  // 解锁密码盘(spec §9——不整页退出)。由 TerminalPane 的 dead 事件驱动。
  function invalidateToken() {
    token.value = '';
  }

  return {
    token, status, loading, error, unlocked,
    refresh, setup, verify, changePin, lock, setEnabled, killall, resetPin,
    invalidateToken,
  };
}
