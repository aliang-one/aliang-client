#!/usr/bin/env python3
"""Convert zh_CN usage-guide.md to styled usage-guide.mdx."""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SRC = ROOT / "zh_CN" / "usage-guide.md"
DST = ROOT / "zh_CN" / "usage-guide.mdx"

# Bare caption line -> image under docs/tutorials/sources/
FIGURE_MAP: dict[str, tuple[str, str]] = {
    "首次安装后的证书状态": (
        "../sources/first_install_status.png",
        "证书管理弹窗：状态为「已安装」，尚未信任",
    ),
    "在钥匙串中设置始终信任": (
        "../sources/change_it_to_trusted.png",
        "钥匙串访问：将「使用此证书时」设为「始终信任」",
    ),
    "证书已信任的成功状态": (
        "../sources/final_success.png",
        "证书管理弹窗：状态为「已信任」",
    ),
}

FRONTMATTER = """---
title: ALiang Gate 使用教程
description: 从安装到高级用法的完整使用指南，涵盖证书、登录、运行模式、CLI 配置与故障排查。
slug: usage-guide
locale: zh_CN
docId: usage-guide
version: "2026-05-22"
category: tutorial
tags:
  - aliang-gate
  - 使用教程
  - 中文
status: published
coverImage: ../sources/final_success.png
relatedDocs:
  - getting-started
---

{/* 后台 MDX 组件约定（可按实际 CMS 映射或删除 export 块） */}
export const Callout = ({ type = "info", title, children }) => {
  const styles = {
    info: "border-sky-200 bg-sky-50 text-sky-900 dark:border-sky-800 dark:bg-sky-950/40 dark:text-sky-100",
    tip: "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-100",
    warning: "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-100",
    danger: "border-rose-200 bg-rose-50 text-rose-900 dark:border-rose-800 dark:bg-rose-950/40 dark:text-rose-100",
  };
  const label = { info: "说明", tip: "提示", warning: "注意", danger: "重要" };
  return (
    <aside className={`my-4 rounded-xl border px-4 py-3 text-sm leading-7 ${styles[type] ?? styles.info}`}>
      <p className="mb-1 text-xs font-bold uppercase tracking-wider opacity-80">{title ?? label[type] ?? "说明"}</p>
      <div>{children}</div>
    </aside>
  );
};

export const Figure = ({ src, alt, caption }) => (
  <figure className="my-6 overflow-hidden rounded-xl border border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-900/50">
    <img src={src} alt={alt} className="w-full object-contain" loading="lazy" />
    {caption ? (
      <figcaption className="border-t border-slate-200 px-4 py-2 text-center text-xs text-slate-500 dark:border-slate-700 dark:text-slate-400">
        {caption}
      </figcaption>
    ) : null}
  </figure>
);

export const Divider = () => <hr className="my-10 border-0 border-t border-dashed border-slate-200 dark:border-slate-700" />;

export const Badge = ({ children, variant = "default" }) => {
  const map = {
    default: "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-200",
    primary: "bg-primary/10 text-primary",
    success: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200",
  };
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-semibold ${map[variant] ?? map.default}`}>
      {children}
    </span>
  );
};

"""


def detect_callout_type(body: str) -> tuple[str, str | None]:
    text = body.strip()
    if text.startswith("💡"):
        return "tip", re.sub(r"^💡\s*\*\*[^*]+\*\*[：:]\s*", "", text).strip()
    for keyword, ctype in (
        ("**重要**", "danger"),
        ("**注意**", "warning"),
        ("**说明**", "info"),
        ("**提示**", "tip"),
        ("**平台说明**", "info"),
        ("**隐私说明**", "info"),
    ):
        if keyword in text[:20]:
            title = keyword.strip("*")
            rest = re.sub(rf"^{re.escape(keyword)}[：:]?\s*", "", text).strip()
            return ctype, title if rest != text else title
    return "info", None


def format_callout_body(text: str) -> str:
    """Keep inline markdown inside callout; escape JSX braces lightly."""
    return text.replace("{", "\\{")


def convert_blockquote(lines: list[str], start: int) -> tuple[str, int]:
    body_lines: list[str] = []
    i = start
    while i < len(lines) and lines[i].startswith("> "):
        body_lines.append(lines[i][2:])
        i += 1
    body = "\n".join(body_lines).strip()
    ctype, title = detect_callout_type(body)
    if title and body.startswith(f"**{title}**"):
        body = re.sub(rf"^\*\*{re.escape(title)}\*\*[：:]?\s*", "", body).strip()
    title_attr = f' title="{title}"' if title else ""
    inner = format_callout_body(body)
    if "\n" in inner:
        inner = "\n".join(f"  {line}" for line in inner.split("\n"))
        block = f'<Callout type="{ctype}"{title_attr}>\n{inner}\n</Callout>'
    else:
        block = f'<Callout type="{ctype}"{title_attr}>\n  {inner}\n</Callout>'
    return block + "\n\n", i


def convert_table(lines: list[str], start: int) -> tuple[str, int]:
    table_lines: list[str] = []
    i = start
    while i < len(lines) and "|" in lines[i]:
        table_lines.append(lines[i])
        i += 1
    if len(table_lines) < 2:
        return "\n".join(table_lines) + "\n", i

    def parse_row(row: str) -> list[str]:
        parts = [c.strip() for c in row.strip().strip("|").split("|")]
        return parts

    headers = parse_row(table_lines[0])
    rows = [parse_row(r) for r in table_lines[2:]]
    thead = "".join(f"<th className=\"px-3 py-2 text-left font-semibold\">{h}</th>" for h in headers)
    tbody_rows = []
    for row in rows:
        cells = "".join(f"<td className=\"px-3 py-2 align-top\">{c}</td>" for c in row)
        tbody_rows.append(f"<tr className=\"border-t border-slate-100 dark:border-slate-800\">{cells}</tr>")
    html = (
        '<div className="my-4 overflow-x-auto rounded-xl border border-slate-200 dark:border-slate-700">\n'
        '  <table className="w-full min-w-[320px] text-sm">\n'
        f"    <thead className=\"bg-slate-50 text-slate-700 dark:bg-slate-800/80 dark:text-slate-200\"><tr>{thead}</tr></thead>\n"
        f"    <tbody>{''.join(tbody_rows)}</tbody>\n"
        "  </table>\n"
        "</div>\n"
    )
    return html + "\n", i


def convert_hr(line: str) -> str:
    if line.strip() == "---":
        return "<Divider />\n\n"
    return line + "\n"


def main() -> None:
    text = SRC.read_text(encoding="utf-8")
    lines = text.splitlines()
    hero = """
<div className="not-prose mb-8 rounded-2xl border border-slate-200 bg-gradient-to-br from-slate-50 via-white to-primary/5 p-6 shadow-sm dark:border-slate-700 dark:from-slate-900 dark:via-slate-900 dark:to-primary/10">
  <div className="mb-3 flex flex-wrap items-center gap-2">
    <Badge variant="primary">完整教程</Badge>
    <Badge>zh_CN</Badge>
    <Badge variant="success">2026-05-22</Badge>
  </div>
  <p className="max-w-2xl text-sm leading-7 text-slate-600 dark:text-slate-300">
    覆盖安装、证书信任、登录、运行模式、CLI 工具、AI 规则与故障排查。适合作为后台帮助中心主文档发布。
  </p>
</div>

"""
    out: list[str] = [FRONTMATTER, hero]

    i = 0
    while i < len(lines):
        line = lines[i]
        stripped = line.strip()

        if stripped in FIGURE_MAP:
            src, alt = FIGURE_MAP[stripped]
            out.append(
                f'<Figure src="{src}" alt="{alt}" caption="{stripped}" />\n\n'
            )
            i += 1
            continue

        if stripped == "---":
            out.append(convert_hr(line))
            i += 1
            continue

        if line.startswith("> "):
            block, i = convert_blockquote(lines, i)
            out.append(block)
            continue

        if stripped.startswith("|") and i + 1 < len(lines) and "---" in lines[i + 1]:
            block, i = convert_table(lines, i)
            out.append(block)
            continue

        if stripped.startswith("## "):
            out.append(f"\n## {stripped[3:]}\n\n")
            i += 1
            continue

        if stripped.startswith("### "):
            out.append(f"\n### {stripped[4:]}\n\n")
            i += 1
            continue

        if stripped.startswith("#### "):
            out.append(f"\n#### {stripped[5:]}\n\n")
            i += 1
            continue

        if stripped.startswith("# "):
            out.append(f"\n# {stripped[2:]}\n\n")
            i += 1
            continue

        out.append(line + "\n")
        i += 1

    content = "".join(out)
    DST.write_text(content, encoding="utf-8")

    import json

    payload_path = ROOT / "zh_CN" / "usage-guide.import.json"
    payload = {
        "title": "ALiang Gate 使用教程",
        "slug": "usage-guide",
        "locale": "zh_CN",
        "docId": "usage-guide",
        "version": "2026-05-22",
        "category": "tutorial",
        "status": "published",
        "format": "mdx",
        "content": content,
        "assets": [
            {"file": "first_install_status.png", "path": "../sources/first_install_status.png"},
            {"file": "change_it_to_trusted.png", "path": "../sources/change_it_to_trusted.png"},
            {"file": "final_success.png", "path": "../sources/final_success.png"},
        ],
    }
    payload_path.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    print(f"Wrote {DST} ({DST.stat().st_size} bytes)")
    print(f"Wrote {payload_path} ({payload_path.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
