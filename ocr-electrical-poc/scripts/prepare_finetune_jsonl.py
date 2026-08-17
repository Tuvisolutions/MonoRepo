#!/usr/bin/env python3
"""Convert dataset/images + dataset/labels into a Gemini-style tuning JSONL.

Each line:
{
  "contents": [
    {"role": "user", "parts": [{"text": "..."}, {"file_uri_placeholder": "images/x.jpg"}]},
    {"role": "model", "parts": [{"text": "<gold json>"}]}
  ]
}

For Vertex upload you will replace file_uri_placeholder with GCS URIs.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_PROMPT = (
    "Analyze this site photo for electrical / gas utility details. "
    "Return structured JSON only."
)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--dataset",
        type=Path,
        default=ROOT / "dataset",
        help="Dataset root with images/ and labels/",
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=ROOT / "exports" / "finetune.jsonl",
    )
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    args = parser.parse_args()

    images_dir = args.dataset / "images"
    labels_dir = args.dataset / "labels"
    if not images_dir.is_dir() or not labels_dir.is_dir():
        raise SystemExit(f"Missing images/ or labels/ under {args.dataset}")

    args.out.parent.mkdir(parents=True, exist_ok=True)
    count = 0
    with args.out.open("w", encoding="utf-8") as fh:
        for label_path in sorted(labels_dir.glob("*.json")):
            stem = label_path.stem
            image_path = None
            for ext in (".jpg", ".jpeg", ".png", ".webp"):
                candidate = images_dir / f"{stem}{ext}"
                if candidate.exists():
                    image_path = candidate
                    break
            if image_path is None:
                print(f"skip {stem}: no matching image")
                continue
            gold = json.loads(label_path.read_text(encoding="utf-8"))
            row = {
                "contents": [
                    {
                        "role": "user",
                        "parts": [
                            {"text": args.prompt},
                            {
                                "file_uri_placeholder": str(
                                    image_path.relative_to(args.dataset)
                                )
                            },
                        ],
                    },
                    {
                        "role": "model",
                        "parts": [{"text": json.dumps(gold, ensure_ascii=False)}],
                    },
                ]
            }
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")
            count += 1

    print(f"Wrote {count} examples → {args.out}")


if __name__ == "__main__":
    main()
