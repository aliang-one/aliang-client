import { createApp } from 'vue';
import App from './App.vue';
import '../assets/styles.css';
import { initializeAuthSession } from './stores/auth';
import { initializeTheme } from './composables/useTheme';

function bootstrap() {
  initializeTheme();
  // Mount the app immediately and restore the session afterwards. Awaiting the
  // session request before mounting left #app empty (blank page) whenever
  // /api/auth/session was slow or hung — App.vue now renders its own loading
  // state until the auth store flips isReady.
  createApp(App).mount('#app');
  // Establish the loopback management session before SSE/GET reconciliation.
  // The app remains mounted while slow or unavailable backends are represented
  // as connectivity state rather than a false logout.
  void initializeAuthSession();
}

bootstrap();
