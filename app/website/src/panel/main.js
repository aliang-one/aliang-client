// main.js — 托盘面板独立入口(与仪表盘 main.js 完全分离)。
//
// 依赖纪律(Task 7 硬约束):本入口绝不 import 仪表盘 App/router/stores/
// composables,以及 xterm/codemirror/图表库等重依赖——面板是托盘弹出的
// 小窗,首帧必须轻;唯一依赖是 Vue 3(既有)与面板自身模块。

import { createApp } from 'vue';
import { isHosted } from './api';
import App from './App.vue';
import './theme.css';

// 宿主态门控(I-1):原生壳(WebView2/WKWebView)加载面板时给 <html> 打
// hosted 标——theme.css 据此把页面底色切成透明,让系统材质层当背景
// (M2 壳 drawsBackground=false,页面不透明渐变会盖住材质)。无宿主
// (M1 浏览器直开)不加类,渐变垫底保留,行为零变化。
if (isHosted()) {
  document.documentElement.classList.add('hosted');
}

createApp(App).mount('#app');
