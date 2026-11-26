// state.js — 面板七态派生纯函数(spec §3 状态矩阵)。
//
// 七态全集:'running' | 'stopped' | 'starting' | 'stopping' | 'restarting'
//          | 'error' | 'logged_out'
//
// 派生优先级(Task 7 指定,spec §3):
//   1. 过渡态优先于远端——用户刚点启/停/重启,本地迁移态压过一切远端快照
//      (连 connLost 也压:点按钮瞬间轮询恰好失败两次不该闪红错误态);
//   2. summary 为 null 或 connLost → error(数据不可信);
//   3. run 子源为 null(服务端降级)→ error(状态头无内容可渲染);
//   4. !is_running && blocked.code 非空 → logged_out(未登录不能启动,
//      拦截原因由状态行承载;运行中带拦截码不算未登录——拦截码只在
//      未运行时有意义);
//   5. is_running → running;否则 stopped。
//
// 本任务只落函数与测试;Task 8 起组件按此态渲染。

// 合法过渡态词表(与 §3「启动中/停止中/重启中」一一对应)。
const TRANSITION_STATES = new Set(['starting', 'stopping', 'restarting']);

// derivePanelState(summary, connLost, localTransition) → 七态之一。
//   summary      parseSummary 归一后的聚合对象(可 null)
//   connLost     useSummary 的连续失败标记
//   localTransition null/''/'starting'/'stopping'/'restarting'
// 纯函数:不读全局、不改入参,未知输入走容错分支而非抛错。
export function derivePanelState(summary, connLost, localTransition) {
  // 1) 过渡态优先于远端(未知过渡态字符串视同无过渡,容错不炸)。
  if (TRANSITION_STATES.has(localTransition)) {
    return localTransition;
  }
  // 2) 无数据 / 连接丢失 → 错误态。
  if (summary === null || summary === undefined || typeof summary !== 'object') {
    return 'error';
  }
  if (connLost) {
    return 'error';
  }
  const run = summary.run;
  // 3) run 子源降级为 null(单源 panic / 缺失)→ 错误态:状态头没有可渲染内容。
  if (run === null || run === undefined || typeof run !== 'object') {
    return 'error';
  }
  const isRunning = run.is_running === true;
  const blockedCode =
    run.blocked && typeof run.blocked === 'object' ? String(run.blocked.code || '') : '';
  // 4) 未登录:未运行且带拦截码。
  if (!isRunning && blockedCode !== '') {
    return 'logged_out';
  }
  // 5) 常规二态。
  return isRunning ? 'running' : 'stopped';
}

// allControlsDisabled(kind) — 传输行「三控件全禁」判定(spec §3 矩阵):
//   过渡态(starting/stopping/restarting):过渡行明确「三控件全禁」,
//     这也是 §6-1 防抖的实现地基(主按钮点击入口先查本函数直接拒绝);
//   logged_out:未登录行明确「三键全禁」(未登录不能启动)。
// 其余三态不全禁:running(停止/重启可用)、stopped(启动可用)、
// error(§3 出错行「启动恢复可用」,不许把用户锁死在错误态外)。
export function allControlsDisabled(kind) {
  return kind === 'starting' || kind === 'stopping' || kind === 'restarting' || kind === 'logged_out';
}
