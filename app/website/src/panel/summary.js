// summary.js — 面板聚合数据的容错解析 + 2s 轮询 composable。
//
// 字段契约以 app/http/services/tray_summary.go 的出参结构体为准(2026-11-27
// 读码对齐,信封由 api.getSummary 返回原始 {code,msg,data,timestamp}):
//   TraySummary    { version, run, ai, agents, endpoints } —— 子源指针可 null
//   TrayRunSummary { is_running, mode, status_desc, listen_addr, blocked{code,msg} }
//   TrayAISummary  { enabled, providers[{key,label,detected,detail}] }
//   TrayAgents     { kernels[{name,active,idle,total_24h,sessions[...]}] }
//   TrayEndpoints  { available, groups[{protocol,current,items[...]}] }
//
// 容错原则:字段缺失不炸、未知字段忽略、坏类型子源降级 null——服务端已对
// 每个子源独立 recover(单源 panic → JSON null),前端同样按「缺就降级」
// 渲染,绝不因一个区块坏掉丢掉整块面板。

import { getCurrentInstance, onUnmounted, ref } from 'vue';
import { getSummary } from './api';

// 轮询周期(Task 7 指定 2s;与服务端 agents 子源 2s 快照 TTL 同量级,
// 两次轮询最多触发一次真实磁盘扫描)。
export const SUMMARY_POLL_MS = 2000;
// 连续失败阈值:连续 2 次失败 → connLost(驱动 §3 错误态)。
export const SUMMARY_FAIL_THRESHOLD = 2;

// parseSummary(raw) — 容错解析。raw 可以是信封 {code,data,...} 或裸聚合对象。
// 返回归一后的 {version, run, ai, agents, endpoints};输入不可用时返回 null。
export function parseSummary(raw) {
  if (raw === null || raw === undefined || typeof raw !== 'object' || Array.isArray(raw)) {
    return null;
  }
  // 信封解包:有 data 键且是对象则取之;信封 code 非 0(如 401)→ 无有效数据。
  let payload = raw;
  if (raw.data !== undefined) {
    if (raw.data === null || typeof raw.data !== 'object' || Array.isArray(raw.data)) {
      return null;
    }
    if (raw.code !== undefined && raw.code !== 0) {
      return null;
    }
    payload = raw.data;
  }

  // 子源归一:非对象(数组/字符串/数字)一律降级 null;缺省 null。
  const sub = (v) => (v !== null && v !== undefined && typeof v === 'object' && !Array.isArray(v) ? v : null);

  // run 子源按契约字段白名单归一:blocked 缺失 → {code:'',msg:''}(下游派生
  // 与渲染不用判空);未知字段丢弃(见上「未知字段忽略」)。
  let run = sub(payload.run);
  if (run !== null) {
    let blocked = sub(run.blocked);
    if (blocked === null) blocked = {};
    run = {
      is_running: run.is_running === true,
      mode: typeof run.mode === 'string' ? run.mode : '',
      status_desc: typeof run.status_desc === 'string' ? run.status_desc : '',
      listen_addr: typeof run.listen_addr === 'string' ? run.listen_addr : '',
      blocked: { code: typeof blocked.code === 'string' ? blocked.code : '', msg: typeof blocked.msg === 'string' ? blocked.msg : '' },
    };
  }

  return {
    version: typeof payload.version === 'string' ? payload.version : '',
    run,
    ai: sub(payload.ai),
    agents: sub(payload.agents),
    endpoints: sub(payload.endpoints),
    // 未知字段刻意不透传(见上「未知字段忽略」)——面板只认契约字段;
    // ai/agents/endpoints 子源保持整体透传,内部字段由各区块组件自行容错。
  };
}

// useSummary() — 轮询 composable:启动即拉一次,此后每 2s 一次;
// 连续 SUMMARY_FAIL_THRESHOLD 次失败 → connLost=true;任何一次成功即恢复
// 清零。组件卸载自动停轮询;非组件环境(Test/外壳)用返回的 stop() 手动停。
// 调用纪律(M-4):仅 App.vue 根壳调用一次(全局唯一数据源);Task 8-11
// 的区块组件一律经 inject('panelData') 取同一份数据,严禁各自再开轮询
// ——2s×N 份轮询会把服务端 agents 快照/磁盘扫放大 N 倍。
// 快照预热(2026-11-28 冷启动体验,用户反馈「列表过一会才出现」):壳每次
// 点击都是全新进程、全新 JS 上下文,首拉(~百 ms)落地前页面是骨架——把上
// 一轮成功 summary 存 localStorage,启动时先渲染快照再被首拉覆盖,列表
// 即刻出现。读写全 try/catch(localStorage 不可用环境静默跳过)。
export const SUMMARY_SNAPSHOT_KEY = 'panel:lastSummary';

export function loadSummarySnapshot() {
  try {
    const raw = localStorage.getItem(SUMMARY_SNAPSHOT_KEY);
    if (raw === null) return null;
    return parseSummary(JSON.parse(raw));
  } catch {
    return null;
  }
}

function saveSummarySnapshot(parsed) {
  try {
    localStorage.setItem(SUMMARY_SNAPSHOT_KEY, JSON.stringify(parsed));
  } catch {
    // 配额满/隐私模式:预热失效但轮询数据链不受影响。
  }
}

export function useSummary({ pollMs = SUMMARY_POLL_MS } = {}) {
  // 启动即用快照预热(可能陈旧,≤2s 内被首拉覆盖;parseSummary 再归一一遍)。
  const summary = ref(loadSummarySnapshot());
  const connLost = ref(false);

  let failCount = 0;
  let timer = null;
  let stopped = false;

  async function poll() {
    if (stopped) return;
    try {
      const raw = await getSummary();
      summary.value = parseSummary(raw);
      if (summary.value !== null) saveSummarySnapshot(summary.value);
      failCount = 0; // 成功即恢复清零
      connLost.value = false;
    } catch {
      failCount += 1;
      if (failCount >= SUMMARY_FAIL_THRESHOLD) {
        connLost.value = true;
      }
      // 失败时保留上次 summary(旧数据比空白好,渲染层可自行打「陈旧」态)。
    }
  }

  function startPolling() {
    if (timer !== null) return;
    timer = setInterval(() => {
      void poll();
    }, pollMs);
  }

  function stop() {
    stopped = true;
    if (timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  }

  startPolling();
  void poll(); // 启动即拉一次,不等第一个 2s

  // 组件环境挂卸载钩子;非组件环境(测试)用 stop() 手动停,避免
  // onUnmounted 在 setup 外调用时的告警。
  if (getCurrentInstance()) {
    onUnmounted(stop);
  }

  return { summary, connLost, stop, refresh: poll };
}
