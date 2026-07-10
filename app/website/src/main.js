import { createApp } from 'vue';
import App from './App.vue';
import '../assets/styles.css';
import { restoreAuthSession, connectSessionEvents } from './stores/auth';
import { initializeTheme } from './composables/useTheme';

function bootstrap() {
  initializeTheme();
  // Mount the app immediately and restore the session afterwards. Awaiting the
  // session request before mounting left #app empty (blank page) whenever
  // /api/auth/session was slow or hung — App.vue now renders its own loading
  // state until the auth store flips isReady.
  createApp(App).mount('#app');
  void restoreAuthSession();
  // Subscribe to identity-transition SSE so login/expiry/recovery reflect
  // instantly (coexists with the restoreAuthSession fetch above and the 5s poll).
  connectSessionEvents();
}

bootstrap();
