import fs from 'node:fs/promises';
import path from 'node:path';

const args = process.argv.slice(2);
const shouldClean = args.includes('--clean');

const websiteRoot = path.resolve(process.cwd());
const inputHtmlPath = path.join(websiteRoot, 'index.html');
const distDir = path.join(websiteRoot, 'dist');
const outputHtmlPath = path.join(distDir, 'index.html');

const MIME_BY_EXT = {
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.ttf': 'font/ttf',
  '.otf': 'font/otf',
  '.eot': 'application/vnd.ms-fontobject',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.avif': 'image/avif'
};

function escapeInlineScript(content) {
  return content.replace(/<\/script>/gi, '<\\/script>');
}

async function ensureDir(dirPath) {
  await fs.mkdir(dirPath, { recursive: true });
}

async function removeDistIfExists() {
  await fs.rm(distDir, { recursive: true, force: true });
}

async function readFileSafe(filePath) {
  return fs.readFile(filePath, 'utf8');
}

function isExternalResource(value) {
  return /^https?:\/\//i.test(value) || /^\/\//.test(value);
}

function getMimeFromPath(resourcePath, contentType = '') {
  if (contentType) {
    return contentType.split(';')[0].trim();
  }
  const cleanPath = resourcePath.split('?')[0].split('#')[0];
  const ext = path.extname(cleanPath).toLowerCase();
  return MIME_BY_EXT[ext] || 'application/octet-stream';
}

async function fetchText(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`Failed to fetch text resource: ${url} (${response.status})`);
  }
  return {
    content: await response.text(),
    contentType: response.headers.get('content-type') || ''
  };
}

async function fetchBinary(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`Failed to fetch binary resource: ${url} (${response.status})`);
  }

  const buffer = Buffer.from(await response.arrayBuffer());
  return {
    buffer,
    contentType: response.headers.get('content-type') || ''
  };
}

function toAbsoluteExternalUrl(url) {
  if (/^\/\//.test(url)) {
    return `https:${url}`;
  }
  return url;
}

async function inlineCssUrlResources(cssContent, context) {
  const urlRegex = /url\(\s*(['"]?)([^'")]+)\1\s*\)/gi;
  let transformed = cssContent;
  const matches = Array.from(cssContent.matchAll(urlRegex));

  for (const match of matches) {
    const fullExpr = match[0];
    const rawUrl = match[2].trim();

    if (!rawUrl || /^data:/i.test(rawUrl) || /^blob:/i.test(rawUrl) || rawUrl.startsWith('#')) {
      continue;
    }

    let mime = '';
    let buffer;

    if (isExternalResource(rawUrl)) {
      const absolute = toAbsoluteExternalUrl(rawUrl);
      const remote = await fetchBinary(absolute);
      buffer = remote.buffer;
      mime = getMimeFromPath(absolute, remote.contentType);
    } else if (context.type === 'external') {
      const absolute = new URL(rawUrl, context.baseUrl).toString();
      const remote = await fetchBinary(absolute);
      buffer = remote.buffer;
      mime = getMimeFromPath(absolute, remote.contentType);
    } else {
      const cleanPath = rawUrl.split('?')[0].split('#')[0];
      const absolutePath = path.resolve(context.baseDir, cleanPath);
      buffer = await fs.readFile(absolutePath);
      mime = getMimeFromPath(cleanPath);
    }

    const dataUri = `url("data:${mime};base64,${buffer.toString('base64')}")`;
    transformed = transformed.replace(fullExpr, dataUri);
  }

  return transformed;
}

function collectMatchesWithIndex(source, regex) {
  const matches = [];
  for (const match of source.matchAll(regex)) {
    if (typeof match.index !== 'number') {
      continue;
    }
    matches.push({
      full: match[0],
      groups: match.slice(1),
      index: match.index
    });
  }
  return matches;
}

async function replaceByIndexedMatches(source, matches, replacer) {
  if (matches.length === 0) {
    return source;
  }

  let result = '';
  let cursor = 0;
  for (const match of matches) {
    result += source.slice(cursor, match.index);
    result += await replacer(match);
    cursor = match.index + match.full.length;
  }
  result += source.slice(cursor);
  return result;
}

async function inlineStylesheets(html) {
  const cssLinkRegex = /<link\s+[^>]*href=["']([^"']+)["'][^>]*rel=["']stylesheet["'][^>]*>/gi;
  const matches = collectMatchesWithIndex(html, cssLinkRegex);

  return replaceByIndexedMatches(html, matches, async (match) => {
    const href = match.groups[0];

    if (/^data:/i.test(href)) {
      return match.full;
    }

    let cssContent = '';
    if (isExternalResource(href)) {
      const absolute = toAbsoluteExternalUrl(href);
      const remote = await fetchText(absolute);
      cssContent = await inlineCssUrlResources(remote.content, { type: 'external', baseUrl: absolute });
    } else {
      const cssPath = path.resolve(websiteRoot, href);
      const localCss = await readFileSafe(cssPath);
      const baseDir = path.dirname(cssPath);
      cssContent = await inlineCssUrlResources(localCss, { type: 'local', baseDir });
    }

    const styleTag = `<style data-inlined-from="${href}">\n${cssContent}\n</style>`;
    return styleTag;
  });
}

async function inlineScripts(html) {
  const scriptRegex = /<script\s+[^>]*src=["']([^"']+)["'][^>]*><\/script>/gi;
  const matches = collectMatchesWithIndex(html, scriptRegex);

  return replaceByIndexedMatches(html, matches, async (match) => {
    const src = match.groups[0];

    if (/^data:/i.test(src)) {
      return match.full;
    }

    let jsContent = '';
    if (isExternalResource(src)) {
      const absolute = toAbsoluteExternalUrl(src);
      const remote = await fetchText(absolute);
      jsContent = remote.content;
    } else {
      const jsPath = path.resolve(websiteRoot, src);
      jsContent = await readFileSafe(jsPath);
    }

    const inlined = `<script data-inlined-from="${src}">\n${escapeInlineScript(jsContent)}\n</script>`;
    return inlined;
  });
}

async function build() {
  if (shouldClean) {
    await removeDistIfExists();
    console.log('dist removed');
    return;
  }

  const originalHtml = await readFileSafe(inputHtmlPath);

  let transformed = originalHtml;
  transformed = await inlineStylesheets(transformed);
  transformed = await inlineScripts(transformed);

  await ensureDir(distDir);
  await fs.writeFile(outputHtmlPath, transformed, 'utf8');

  console.log(`single-file build generated: ${outputHtmlPath}`);
}

build().catch((error) => {
  console.error('build failed:', error);
  process.exitCode = 1;
});
