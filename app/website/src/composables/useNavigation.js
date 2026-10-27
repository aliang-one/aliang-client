import { ref } from 'vue';

const currentPage = ref('dashboard');

// 聊天页的 provider 预置：仪表盘「AliangCode 聊天」入口写入 'agentcore'，
// ChatPage 新建会话时读取。模块级 = 跨组件共享；浏览器刷新自然重置为空，
// 不引入陈旧状态（不持久化）。
const chatProviderPreset = ref('');

export function useNavigation() {
  function showPage(page) {
    currentPage.value = page;
  }

  function showSettings() {
    showPage('settings');
  }

  function showAgent() {
    showPage('agent');
  }

  function showUserCenter() {
    showPage('user');
  }

  function showLogs() {
    showPage('log');
  }

  function showDashboard() {
    showPage('dashboard');
  }

  function showChat(options = {}) {
    // 只认字符串：provider 是封闭字符串词表，非字符串（Number/对象）一律归一为空串，
    // 否则 `String({})` 会写出 '[object object]' 这种脏预置传进 ChatPage。
    const provider = options?.provider;
    chatProviderPreset.value = typeof provider === 'string' ? provider.trim().toLowerCase() : '';
    showPage('chat');
  }

  return {
    currentPage,
    chatProviderPreset,
    showPage,
    showSettings,
    showAgent,
    showUserCenter,
    showLogs,
    showDashboard,
    showChat
  };
}
