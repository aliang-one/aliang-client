// 文件树图标嗅探(P3.6,计划 2026-11-22-web-terminal-phase23「嗅探出 icon」):
// 纯前端扩展名→material-symbols 图标声明式映射,零额外请求/零内容探测(内容
// 级二进制判定归 fsRead 回包的 binary 位,与此处扩展名嗅探分工)。iconFor 纯
// 函数独立导出供 vitest 直测(先例 cascadePos/floatingWindowGeom)。

// 图片扩展 → image(预览侧二进制徽章仍由 fsRead 决定,图标只管观感归类)
const IMAGE_EXTS = new Set([
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'tiff', 'tif', 'heic', 'avif',
]);

// 压缩包 → folder_zip
const ARCHIVE_EXTS = new Set(['zip', 'tar', 'gz', 'tgz', 'rar', '7z', 'bz2', 'xz', 'zst']);

// 代码/结构化文本 → code(与 FilePreview EXT_LANG 高亮集合同向但不必逐项对齐:
// 图标是粗分类,未命中的代码扩展退 description 不影响预览高亮)
const CODE_EXTS = new Set([
  'js', 'mjs', 'cjs', 'ts', 'tsx', 'jsx', 'vue', 'svelte',
  'py', 'rb', 'rs', 'go', 'java', 'kt', 'swift', 'php', 'c', 'h', 'cpp', 'hpp',
  'sh', 'bash', 'zsh', 'sql',
  'html', 'htm', 'css', 'scss', 'less',
  'json', 'yaml', 'yml', 'toml', 'xml', 'md',
]);

// 条目 → material-symbols 图标名。目录 folder(行内展开态变色由调用方自理);
// symlink 目标不可判定(可为目录/文件),统一通用文件图标——行尾 ↗ 徽章已
// 标示链接性。防御:null/非对象/无名 → description;点文件(dot≤0,如
// .gitignore)与尾点名无扩展可言 → description。
export function iconFor(entry) {
  if (!entry || typeof entry !== 'object') return 'description';
  if (entry.type === 'dir') return 'folder';
  if (entry.type === 'symlink') return 'description';
  const name = typeof entry.name === 'string' ? entry.name : '';
  const dot = name.lastIndexOf('.');
  if (dot <= 0 || dot === name.length - 1) return 'description';
  const ext = name.slice(dot + 1).toLowerCase();
  if (IMAGE_EXTS.has(ext)) return 'image';
  if (ARCHIVE_EXTS.has(ext)) return 'folder_zip';
  if (CODE_EXTS.has(ext)) return 'code';
  return 'description';
}
