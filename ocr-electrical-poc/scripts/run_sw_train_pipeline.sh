#!/usr/bin/env bash
# End-to-end: pull electrical photos from SW prod VM → label → train exports.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
source .venv/bin/activate

LIMIT="${LIMIT:-50}"
MODE="${MODE:-metadata}"   # metadata | gemini | hybrid
OUT="${OUT:-dataset/sw_prod}"

echo "== 1) Fetch from prod (SSH) limit=$LIMIT =="
python scripts/fetch_sw_electrical_photos.py \
  --ssh-prod \
  --limit "$LIMIT" \
  --prefer-nmi \
  --fields photo electricityMeterPhoto switchboardPhoto \
  --out "$OUT"

echo "== 2) Build labels mode=$MODE =="
python scripts/build_labels_from_metadata.py \
  --manifest "$OUT/manifest.jsonl" \
  --dataset "$OUT" \
  --mode "$MODE" \
  --strip-source

echo "== 3) Train / export backends =="
python scripts/train_models.py \
  --dataset "$OUT" \
  --backends gemini-jsonl,evaluate \
  --limit "$LIMIT" \
  --out-dir exports/train_runs

echo "Done. See $OUT and exports/train_runs/"
