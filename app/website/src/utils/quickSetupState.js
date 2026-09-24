export function createLatestRenderGuard() {
  let sequence = 0;
  return {
    begin() {
      sequence += 1;
      return sequence;
    },
    invalidate() {
      sequence += 1;
    },
    isCurrent(requestId) {
      return requestId === sequence;
    },
    canCommit(requestId, dirty) {
      return requestId === sequence && !dirty;
    },
  };
}

export function isBuiltInQuickSetupSoftware(software) {
  return Boolean(software && !software.isCustom);
}

export function snapshotQuickSetupFiles(files) {
  return Array.isArray(files) ? files.map((file) => ({ ...file })) : [];
}

// 过滤出本机已安装的内置 software；custom 模板始终保留（spec §9.1）。
// list 项：{ code, installed?, isCustom? }。custom 判定兼容 isCustom 标记与 custom- 前缀。
export function filterInstalledQuickSetupSoftwares(list) {
  return (list || []).filter((s) => {
    const isCustom = s.isCustom || String(s.code || '').startsWith('custom-');
    return isCustom || s.installed === true;
  });
}

// 每 software 独立的接入模式状态（local/public），默认 public（spec §2）。
export function createQuickSetupModeState() {
  const modes = new Map();
  return {
    modeOf(code) {
      return modes.get(code) || 'public';
    },
    setMode(code, mode) {
      if (mode !== 'local' && mode !== 'public') return;
      modes.set(code, mode);
    },
  };
}

// 组合模板渲染：顺序替换三占位符；替换后的值不再扫描（值含 {{ 也不会二次展开）。
export function renderComboContent(content, variables) {
  let out = String(content ?? '');
  for (const [key, value] of Object.entries(variables || {})) {
    out = out.split(`{{${key}}}`).join(String(value ?? ''));
  }
  return out;
}

// 列出内容中未替换的占位符名（如 ['api_key']）——apply 前端预检用。
export function findUnresolvedPlaceholders(content) {
  const re = /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g;
  const names = [];
  let m;
  while ((m = re.exec(String(content ?? ''))) !== null) {
    names.push(m[1]);
  }
  return names;
}
