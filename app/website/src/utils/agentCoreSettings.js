// AliangCode 内核设置(仪表盘)纯逻辑:校验与归一。零依赖,vitest 直接
// 覆盖;UI 装配在 settings/AgentCoreSettings.vue。
// ⚠️ endpoint 二期 Task 8(spec §7.3/§9):agentcore_llm(端点)层的前端
// 纯函数(normalizeAgentCoreLLMConfig/validateAgentCoreLLMConfig/
// applyAgentCoreLLMClearing)已退役删除——出网端点由 endpoint 管理接管
// (utils/endpointSettings.js),Go POST 白名单保留接受该键避免 403(值被
// 忽略);前端归一链不再读写该键。本文件只剩 customer.agentcore(行为调优)
// 与 customer.peer_mcp 两层。

// ---- customer.agentcore(行为调优) ----
// 校验规则与后端
// AgentCoreTuningConfig.Validate(processor/config/types.go)同源,两端不许分叉:
// 0/未填 = 用内核默认,1..50 = 显式阈值,负数/非整数/越界拒绝。
// ⚠️ 与 agentcore_llm 的字符串不同,数字的「清空」天然可达:归一成 0 后,
// buildConfigPatch 的 Object.is 比较会把 0≠旧值写进 patch(0 显式落盘,
// 后端 omitempty 存储时抹掉 = 回到默认),所以这里不需要 applyXxxClearing。

// 服务端载荷 → 归一线格式。非对象/缺键输出 {max_identical_tool_turns: 0}
// (= 未配置,与 Go 侧 nil 层等价)。非法数字(负数/非整数)归一成 0——归一层
// 不报错,报错是 validate 的职责;这里只保证输出永远是合法线格式。
// ⚠️ mcp_enabled/skills_enabled(全局总闸,2026-10-31)、memory_enabled(记忆
// 总开关,2026-11-09)与 ai_mcp(agent-as-MCP 工具面,2026-11-10)必须在此透传:
// 本函数的输出经 normalizeCustomerConfig →
// buildCustomerConfigPatch 原样进 POST patch 与 GET 回显——在这里剥掉的话,
// 全局开关卡「保存 HTTP 200 但永不落盘、刷新恒显默认开」(2026-10-31 验收
// 缺陷①)。布尔键显式透传,缺键不出现(= 未配置,后端 nil 视为开启,与 Go
// 侧三态等价;MemoryEnabled *bool json:"memory_enabled,omitempty"、
// AIMCP *bool json:"ai_mcp,omitempty")。
export function normalizeAgentCoreTuning(source) {
  let turns = 0;
  let mcpEnabled;
  let skillsEnabled;
  let memoryEnabled;
  let aiMcpEnabled;
  if (source && typeof source === 'object' && !Array.isArray(source)) {
    const raw = source.max_identical_tool_turns;
    if (typeof raw === 'number' && Number.isInteger(raw) && raw > 0) {
      turns = raw;
    }
    if (typeof source.mcp_enabled === 'boolean') {
      mcpEnabled = source.mcp_enabled;
    }
    if (typeof source.skills_enabled === 'boolean') {
      skillsEnabled = source.skills_enabled;
    }
    if (typeof source.memory_enabled === 'boolean') {
      memoryEnabled = source.memory_enabled;
    }
    if (typeof source.ai_mcp === 'boolean') {
      aiMcpEnabled = source.ai_mcp;
    }
  }
  return {
    max_identical_tool_turns: turns,
    ...(mcpEnabled === undefined ? {} : { mcp_enabled: mcpEnabled }),
    ...(skillsEnabled === undefined ? {} : { skills_enabled: skillsEnabled }),
    ...(memoryEnabled === undefined ? {} : { memory_enabled: memoryEnabled }),
    ...(aiMcpEnabled === undefined ? {} : { ai_mcp: aiMcpEnabled }),
  };
}

// ---- customer.peer_mcp(aliang-peer 跨设备工具面总开关) ----
// 三态与 mcp_enabled 同族:布尔显式落盘;缺键/非布尔 → undefined(不进
// patch,后端 nil 视为开启)。⚠️ 必须在 SettingsPage.normalizeCustomerConfig
// 挂键,否则「保存 HTTP 200 但永不落盘」(2026-10-31 验收缺陷①同族)。
// 四键三态:显式布尔才产出对应键(缺键/非布尔不进 patch,不清用户配置);
// 全空对象 → undefined。enable=总开关,其余三键 nil=跟随总开关(2026-11-05 细分)。
export function normalizePeerMCPConfig(source) {
  if (!source || typeof source !== 'object' || Array.isArray(source)) {
    return undefined;
  }
  const out = {};
  for (const key of ['enable', 'agentcore_enabled', 'claude_enabled', 'codex_enabled']) {
    if (typeof source[key] === 'boolean') out[key] = source[key];
  }
  return Object.keys(out).length ? out : undefined;
}

// 表单值('' 或数字字符串)→ 校验。返回 ''(合法)或 i18n 键。空串与 0 都是
// 「用默认」,合法;正整数上限 50 与后端 Validate 对齐。
export function validateAgentCoreTuning(maxTurns) {
  const raw = String(maxTurns ?? '').trim();
  if (!raw) return '';
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 0) return 'agentCore_error_guardInvalid';
  if (n === 0) return '';
  if (n > 50) return 'agentCore_error_guardInvalid';
  return '';
}

// 表单值 → 线格式数字。空串/0 → 0(= 默认,后端 omitempty 存储时抹掉)。
// 非法输入不会被提交(validate 挡在 save 前),这里兜底归 0。
export function agentCoreTuningToLineFormat(maxTurns) {
  const raw = String(maxTurns ?? '').trim();
  if (!raw) return 0;
  const n = Number(raw);
  if (!Number.isInteger(n) || n <= 0) return 0;
  return n;
}
