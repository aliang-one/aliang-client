import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  test: {
    include: ['src/**/*.test.js'],
    environment: 'jsdom',
  },
  build: {
    outDir: 'dist',
    // 双入口:仪表盘(index.html)与托盘面板(panel.html,Task 7)各自独立
    // 打包——面板入口不进仪表盘 chunk 图,仪表盘重依赖也不进面板产物。
    rollupOptions: {
      input: {
        main: 'index.html',
        panel: 'panel.html'
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:56431',
        changeOrigin: true,
        ws: true
      }
    }
  }
});
