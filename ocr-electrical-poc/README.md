# Tuvi Electrical Circuit OCR POC

Standalone Gemini vision POC that turns utility / meter-board photos into **structured JSON** (NMI, fuses, brands, unlabeled objects like gas pipes).

## Quick start

```bash
cd /Users/praveenmaurya/Desktop/Tuvi/MonoRepo/ocr-electrical-poc
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
cp .env.example .env
# Put your Gemini API key in .env → GEMINI_API_KEY=...
# Default model: gemini-3.1-flash-lite (override with GEMINI_MODEL)
uvicorn app.main:app --host 0.0.0.0 --port 8090 --reload
```

Open **http://localhost:8090**

## CLI smoke test

```bash
source .venv/bin/activate
python scripts/smoke_test.py examples/meter_panel_sample.png
```

## API

- `GET /health` — key configured?
- `POST /analyze` — multipart field `file` (JPEG/PNG/WebP)

## Train from Sustainability Wise prod photos

Prod EcoAudit has ~3.8k electrical photos (`main_switchboard`, `additional_switchboard`, `solar_pv`, `general_electricity`) with metadata (`site_nmi`, captions, entity type).

### One-shot pipeline (SSH to prod VM)

```bash
source .venv/bin/activate
chmod +x scripts/run_sw_train_pipeline.sh
LIMIT=50 MODE=metadata ./scripts/run_sw_train_pipeline.sh
```

- `MODE=metadata` — fast weak labels from DB NMI / captions
- `MODE=hybrid` — Gemini vision labels + DB NMI override (needs `GEMINI_API_KEY`)
- `MODE=gemini` — Gemini-only labels

### Manual steps

```bash
# 1) Pull images + manifest from prod
python scripts/fetch_sw_electrical_photos.py --ssh-prod --limit 100 --prefer-nmi \
  --fields photo electricityMeterPhoto switchboardPhoto \
  --out dataset/sw_prod

# 2) Build ElectricalAnalysis labels
python scripts/build_labels_from_metadata.py \
  --manifest dataset/sw_prod/manifest.jsonl \
  --dataset dataset/sw_prod --mode hybrid --strip-source

# 3) Train / export several backends
python scripts/train_models.py --dataset dataset/sw_prod \
  --backends gemini-jsonl,evaluate,gemini-tune,trocr \
  --out-dir exports/train_runs
```

| Backend | What it does |
|---------|----------------|
| `gemini-jsonl` | Multimodal JSONL for Vertex / Gemini SFT |
| `gemini-tune` | Attempts API tune job (often needs Vertex + GCS) |
| `trocr` | Fine-tunes HuggingFace TrOCR on `raw_ocr_text` (`pip install torch transformers`) |
| `evaluate` | Scores current `GEMINI_MODEL` NMI exact-match vs gold |

Also:

```bash
python scripts/prepare_finetune_jsonl.py --dataset dataset/sw_prod --out exports/finetune.jsonl
```

## Security

Never commit `.env` or prod Spaces/DB credentials. If a key was pasted in chat, rotate it.
