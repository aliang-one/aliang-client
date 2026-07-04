#!/usr/bin/env bash
# 从 SVG 源重新生成全部 App / 托盘图标资产。
#
# 权威源（形状/配色以此为准）：
#   app/aliang-logo.svg            绿色激活态
#   app/aliang-logo-inactive.svg   置灰 inactive 态
#
# 产物：
#   scripts/Aliang.icns              macOS .app 图标
#   scripts/desktop-logo.ico         Windows MSI 图标
#   scripts/logo.png                 Linux deb 图标
#   app/tray/icon-active.{png,ico}   托盘激活态 (macOS/Linux PNG, Windows ICO)
#   app/tray/icon-inactive.{png,ico} 托盘置灰态
#   app/tray/active.svg inactive.svg 与源同步
#   app/website/public/icon.svg      dashboard favicon
#
# 依赖：ImageMagick (magick)、iconutil (macOS 自带)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
ACTIVE_SRC="$PROJECT_DIR/app/aliang-logo.svg"
INACTIVE_SRC="$PROJECT_DIR/app/aliang-logo-inactive.svg"

command -v magick      >/dev/null || { echo "需要 ImageMagick: brew install imagemagick"; exit 1; }
command -v rsvg-convert >/dev/null || { echo "需要 librsvg: brew install librsvg"; exit 1; }
command -v iconutil    >/dev/null || { echo "需要 iconutil (macOS 自带)"; exit 1; }

# 注意：magick 内置 SVG 读取对 linearGradient 渲染异常（输出空图），
# 因此 SVG→PNG 统一交给 rsvg-convert，magick 仅负责 extent 居中与 ICO 封装。

# render_square <src_svg> <size> <out_png>
# SVG → 正方形透明 PNG（保持原始比例居中，避免 ICO/ICNS 形变）
render_square() {
  local tmp; tmp="$(mktemp -t genicons).png"
  rsvg-convert -w "$2" "$1" -o "$tmp"
  magick "$tmp" -background none -gravity center -extent "${2}x${2}" "$3"
  rm -f "$tmp"
}

# make_ico <src_svg> <max> <out_ico> <sizes>
make_ico() {
  local tmp; tmp="$(mktemp -t genicons).png"
  rsvg-convert -w "$2" "$1" -o "$tmp"
  magick "$tmp" -background none -gravity center -extent "${2}x${2}" \
    -define "icon:auto-resize=$4" "$3"
  rm -f "$tmp"
}

echo "==> 同步托盘 / website SVG 源"
cp "$ACTIVE_SRC"   "$PROJECT_DIR/app/tray/active.svg"
cp "$INACTIVE_SRC" "$PROJECT_DIR/app/tray/inactive.svg"
cp "$ACTIVE_SRC"   "$PROJECT_DIR/app/website/public/icon.svg"

echo "==> Linux 打包图标 scripts/logo.png (256)"
render_square "$ACTIVE_SRC" 256 "$SCRIPT_DIR/logo.png"

echo "==> Windows 打包图标 scripts/desktop-logo.ico"
make_ico "$ACTIVE_SRC" 256 "$SCRIPT_DIR/desktop-logo.ico" "256,128,64,48,32,16"

echo "==> macOS 打包图标 scripts/Aliang.icns"
ICONSET_DIR="$(mktemp -d)"
ICONSET="$ICONSET_DIR/Aliang.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  render_square "$ACTIVE_SRC" "$s"       "$ICONSET/icon_${s}x${s}.png"
  render_square "$ACTIVE_SRC" "$((s*2))" "$ICONSET/icon_${s}x${s}@2x.png"
done
rm -f "$SCRIPT_DIR/Aliang.icns"
iconutil -c icns "$ICONSET" -o "$SCRIPT_DIR/Aliang.icns"
rm -rf "$ICONSET_DIR"

echo "==> 托盘主图标 active(绿) / inactive(灰)"
render_square "$ACTIVE_SRC"   128 "$PROJECT_DIR/app/tray/icon-active.png"
render_square "$INACTIVE_SRC" 128 "$PROJECT_DIR/app/tray/icon-inactive.png"
make_ico "$ACTIVE_SRC"   128 "$PROJECT_DIR/app/tray/icon-active.ico"   "128,64,48,32,24,16"
make_ico "$INACTIVE_SRC" 128 "$PROJECT_DIR/app/tray/icon-inactive.ico" "128,64,48,32,24,16"

echo "==> 完成。提示: app/website/dist 需重新 npm run build 才会更新 favicon。"
