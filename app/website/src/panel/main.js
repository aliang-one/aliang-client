// main.js — 托盘面板独立入口(与仪表盘 main.js 完全分离)。
//
// 依赖纪律(Task 7 硬约束):本入口绝不 import 仪表盘 App/router/stores/
// composables,以及 xterm/codemirror/图表库等重依赖——面板是托盘弹出的
// 小窗,首帧必须轻;唯一依赖是 Vue 3(既有)与面板自身模块。

import { createApp } from 'vue';
import App from './App.vue';
import './theme.css';

createApp(App).mount('#app');
