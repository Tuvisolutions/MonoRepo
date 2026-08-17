# Fine-tune dataset

## Layout

```
dataset/
  images/          # meter_001.jpg, <photo_uuid>.jpg, ...
  labels/          # matching ElectricalAnalysis JSON
  sw_prod/         # pulled from Sustainability Wise (gitignored bulk)
    images/
    labels/
    manifest.jsonl
    metadata.json
```

Each label JSON should use the same fields as `app/schema.py` / `examples/meter_panel_sample.gold.json`.

## Sustainability Wise source

Electrical EcoAudit photos live in prod `photo_registry` (Spaces bytes + Postgres metadata).

Useful ground-truth fields:

- `ea_main_switchboards.site_nmi` → `identifiers.nmi`
- `photo_descs` captions → `handwritten_labels` / summary
- `entity_type` / `field_name` → `scene_type` hints

`photo_descs` alone is **not** full OCR gold — use `MODE=hybrid` (Gemini + DB NMI) before a serious fine-tune.

## Build JSONL

```bash
python scripts/prepare_finetune_jsonl.py --dataset dataset/sw_prod --out exports/finetune.jsonl
```

## Suggested mix (50–200 images)

- Main switchboard `photo` with NMI filled
- Solar `electricityMeterPhoto` / `switchboardPhoto`
- Extra photos only after primary shots are labeled
