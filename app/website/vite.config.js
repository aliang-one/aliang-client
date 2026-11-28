import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  // 远程协助 P2(Task 3):相对 base——仪表盘须能在任意路径前缀下挂载
  // (管理端镜像 /assist/service/,Task 2 同构代理剥前缀反代本机真身)。
  // 构建产物引用 ./assets/...,根挂载(/)解析为 /assets/...(原语义不变),
  // 镜像挂载解析为 /assist/service/assets/...(代理剥前缀后命中同一真身)。
  // 注:相对 base 下 vite dev 不支持挂前缀,前缀挂载冒烟走构建产物+代理。
  base: './',
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
