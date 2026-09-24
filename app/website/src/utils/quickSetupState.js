// 组合模板渲染与占位符预检（quick-config v3）。

// 组合模板渲染：单次正则替换（与 findUnresolvedPlaceholders/后端校验同一空白容忍
// 语义）；替换回调的返回值不会被再次扫描——变量值含 {{...}} 也不会二次展开。
export function renderComboContent(content, variables) {
  const vars = variables || {};
  return String(content ?? '').replace(
    /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g,
    (m, key) => (Object.prototype.hasOwnProperty.call(vars, key) ? String(vars[key] ?? '') : m),
  );
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
