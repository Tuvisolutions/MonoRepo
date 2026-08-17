#!/usr/bin/env python3
"""Build ElectricalAnalysis label JSON from SW photo metadata (+ optional Gemini).

Modes:
  metadata  — weak labels from site_nmi / caption / entity_type (fast, no API)
  gemini    — call Gemini vision for each image (stronger training targets)
  hybrid    — metadata seed, then Gemini fill; NMI from DB wins when present

Writes:
  <dataset>/labels/<photo_id>.json
  copies or links images into <dataset>/images if --link-images

Example:
  python scripts/build_labels_from_metadata.py \\
    --manifest dataset/sw_prod/manifest.jsonl \\
    --dataset dataset/sw_prod --mode hybrid
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "scripts"))

from dotenv import load_dotenv

from sw_common import read_jsonl, scene_type_for_entity  # noqa: E402


def metadata_label(row: dict[str, Any]) -> dict[str, Any]:
    entity_type = row.get("entity_type") or "unknown"
    field_name = row.get("field_name") or ""
    nmi = row.get("site_nmi")
    caption = row.get("caption")
    site = row.get("audit_site_name") or ""
    entity_name = row.get("entity_name") or ""
    summary_bits = [
        bit
        for bit in [
            f"{entity_type.replace('_', ' ')} photo",
            f"field={field_name}" if field_name else "",
            f"site={site}" if site else "",
            f"entity={entity_name}" if entity_name else "",
            f"caption={caption}" if caption else "",
            f"nmi={nmi}" if nmi else "",
        ]
        if bit
    ]
    handwritten = []
    if caption:
        handwritten.append(caption)
    if nmi:
        handwritten.append(f"NMI: {nmi}")

    other_ids = []
    if entity_name:
        other_ids.append(entity_name)

    return {
        "scene_type": scene_type_for_entity(entity_type, field_name),
        "summary": "; ".join(summary_bits) or "Electrical site photo",
        "identifiers": {"nmi": nmi, "other_ids": other_ids},
        "meter": {"brand": None, "model": None, "notes": []},
        "protection": {"fuses": [], "notes": []},
        "components": [
            {
                "type": "other",
                "label": entity_name or entity_type.replace("_", " "),
                "confidence": 0.4,
            }
        ],
        "handwritten_labels": handwritten,
        "warnings": [
            "Weak label from Sustainability Wise metadata — verify before production fine-tune"
        ],
        "unlabeled_detections": [],
        "raw_ocr_text": " | ".join(handwritten),
        "_source": {
            "photo_id": row.get("photo_id"),
            "mode": "metadata",
            "storage_key": row.get("storage_key"),
            "field_name": field_name,
            "entity_type": entity_type,
        },
    }


def gemini_label(image_path: Path) -> dict[str, Any]:
    from app.gemini_client import analyze_image

    suffix = image_path.suffix.lower()
    mime = {
        ".jpg": "image/jpeg",
        ".jpeg": "image/jpeg",
        ".png": "image/png",
        ".webp": "image/webp",
    }.get(suffix, "image/jpeg")
    analysis = analyze_image(image_path.read_bytes(), mime_type=mime)
    data = analysis.model_dump()
    data["_source"] = {"mode": "gemini", "image": str(image_path.name)}
    return data


def merge_hybrid(meta: dict[str, Any], gem: dict[str, Any]) -> dict[str, Any]:
    out = dict(gem)
    # DB NMI wins when present (ground-truth from equipment form).
    meta_nmi = (meta.get("identifiers") or {}).get("nmi")
    if meta_nmi:
        out.setdefault("identifiers", {})
        out["identifiers"]["nmi"] = meta_nmi
        other = list(out["identifiers"].get("other_ids") or [])
        for oid in (meta.get("identifiers") or {}).get("other_ids") or []:
            if oid not in other:
                other.append(oid)
        out["identifiers"]["other_ids"] = other
    # Keep provenance.
    out["_source"] = {
        "mode": "hybrid",
        "photo_id": (meta.get("_source") or {}).get("photo_id"),
        "storage_key": (meta.get("_source") or {}).get("storage_key"),
        "gemini": True,
        "metadata_nmi": meta_nmi,
    }
    # Drop training-only warnings from weak metadata when Gemini ran.
    warnings = [w for w in (out.get("warnings") or []) if "Weak label" not in w]
    out["warnings"] = warnings
    return out


def resolve_image(manifest_dir: Path, row: dict[str, Any]) -> Path | None:
    rel = row.get("image_relpath")
    if rel:
        path = manifest_dir / rel
        if path.is_file():
            return path
    photo_id = row.get("photo_id")
    images = manifest_dir / "images"
    if photo_id and images.is_dir():
        for ext in (".jpg", ".jpeg", ".png", ".webp"):
            candidate = images / f"{photo_id}{ext}"
            if candidate.is_file():
                return candidate
    return None


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--dataset", type=Path, default=None, help="Defaults to manifest parent")
    parser.add_argument(
        "--mode",
        choices=("metadata", "gemini", "hybrid"),
        default="hybrid",
    )
    parser.add_argument("--limit", type=int, default=0, help="0 = all")
    parser.add_argument(
        "--strip-source",
        action="store_true",
        help="Remove _source from label JSON (cleaner for fine-tune)",
    )
    args = parser.parse_args()

    load_dotenv(ROOT / ".env")
    manifest_path = args.manifest
    dataset = args.dataset or manifest_path.parent
    labels_dir = dataset / "labels"
    images_out = dataset / "images"
    labels_dir.mkdir(parents=True, exist_ok=True)
    images_out.mkdir(parents=True, exist_ok=True)

    rows = read_jsonl(manifest_path)
    if args.limit:
        rows = rows[: args.limit]

    ok = 0
    skipped = 0
    for index, row in enumerate(rows, start=1):
        image_path = resolve_image(manifest_path.parent, row)
        if image_path is None:
            print(f"skip {row.get('photo_id')}: image missing")
            skipped += 1
            continue

        # Ensure image lives under dataset/images with stable stem = photo_id.
        stem = row.get("photo_id") or image_path.stem
        dest_image = images_out / f"{stem}{image_path.suffix.lower()}"
        if dest_image.resolve() != image_path.resolve():
            if not dest_image.exists():
                shutil.copy2(image_path, dest_image)

        try:
            if args.mode == "metadata":
                label = metadata_label(row)
            elif args.mode == "gemini":
                label = gemini_label(dest_image)
                # Still stamp DB NMI when available.
                if row.get("site_nmi"):
                    label.setdefault("identifiers", {})["nmi"] = row["site_nmi"]
            else:
                meta = metadata_label(row)
                gem = gemini_label(dest_image)
                label = merge_hybrid(meta, gem)
        except Exception as exc:  # noqa: BLE001
            print(f"FAIL {stem}: {exc}")
            skipped += 1
            continue

        if args.strip_source:
            label.pop("_source", None)

        (labels_dir / f"{stem}.json").write_text(
            json.dumps(label, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )
        ok += 1
        if index % 5 == 0 or index == len(rows):
            print(f"  labeled {ok}/{len(rows)} (skipped {skipped})")

    print(json.dumps({"labeled": ok, "skipped": skipped, "dataset": str(dataset)}, indent=2))


if __name__ == "__main__":
    main()
