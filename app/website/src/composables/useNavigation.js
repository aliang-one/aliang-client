import { ref } from 'vue';

const currentPage = ref('dashboard');

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

  function showChat() {
    showPage('chat');
  }

  return {
    currentPage,
    showPage,
    showSettings,
    showAgent,
    showUserCenter,
    showLogs,
    showDashboard,
    showChat
  };
}
