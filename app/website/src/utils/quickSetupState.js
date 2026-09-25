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

// 行级变更标记（轻量多集对比，非对齐 diff）：返回 { leftFlags, rightFlags }，
// true 表示该行在另一侧没有可匹配的行（变更行）。重复行按出现次数逐个对账——
// 左侧两行 a、右侧一行 a 时左侧恰一行标 true。nullish 输入按空内容处理。
export function diffChangedLines(leftContent, rightContent) {
  const leftLines = String(leftContent ?? '').split('\n');
  const rightLines = String(rightContent ?? '').split('\n');
  const leftFlags = flagUnmatchedLines(leftLines, rightLines);
  const rightFlags = flagUnmatchedLines(rightLines, leftLines);
  return { leftFlags, rightFlags };
}

// 在 otherLines 的多集中逐个核销 lines 的每一行；核销失败（另一侧无剩余同名行）即变更行
function flagUnmatchedLines(lines, otherLines) {
  const pool = new Map();
  for (const line of otherLines) {
    pool.set(line, (pool.get(line) || 0) + 1);
  }
  return lines.map((line) => {
    const count = pool.get(line) || 0;
    if (count > 0) {
      pool.set(line, count - 1);
      return false;
    }
    return true;
  });
}
