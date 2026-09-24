#!/usr/bin/env bash
# Render every M.A.R.C.U.S. diagram HTML in this folder to PDF using
# headless Chrome. Output lands in ~/Documents with the MARCUS- prefix.
#   usage: ./render.sh            (all diagrams)
#          ./render.sh portrait   (only files matching "portrait")
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="${MARCUS_OUT:-$HOME/Documents}"
CHROME="/c/Program Files/Google/Chrome/Application/chrome.exe"

declare -A NAMES=(
  [network-flow-portrait]="MARCUS-Network-Flow-Portrait"
  [network-flow-customer]="MARCUS-Network-Flow-Customer"
  [multi-site-flow]="MARCUS-Multi-Site-Flow"
  [infrastructure-diagram]="MARCUS-Infrastructure-Diagram"
  [low-level-architecture]="MARCUS-Low-Level-Architecture"
)

filter="${1:-}"
for src in "$HERE"/*.html; do
  base="$(basename "$src" .html)"
  [[ -n "$filter" && "$base" != *"$filter"* ]] && continue
  name="${NAMES[$base]:-MARCUS-$base}"
  win_src="$(cygpath -w "$src")"
  win_out="$(cygpath -w "$OUT/$name.pdf")"
  "$CHROME" --headless=new --disable-gpu --no-pdf-header-footer \
    --print-to-pdf="$win_out" "file:///${win_src//\\//}" 2>/dev/null
  echo "rendered $OUT/$name.pdf"
done
