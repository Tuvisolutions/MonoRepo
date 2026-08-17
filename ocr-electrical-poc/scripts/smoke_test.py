#!/usr/bin/env python3
"""Smoke-test Gemini electrical OCR against a local image."""

from __future__ import annotations

import json
import mimetypes
import sys
from pathlib import Path

from dotenv import load_dotenv

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
load_dotenv(ROOT / ".env")

from app.gemini_client import analyze_image  # noqa: E402


def main() -> None:
    if len(sys.argv) < 2:
        path = ROOT / "examples" / "meter_panel_sample.png"
    else:
        path = Path(sys.argv[1])
    if not path.exists():
        raise SystemExit(f"Image not found: {path}")

    mime, _ = mimetypes.guess_type(str(path))
    mime = mime or "image/jpeg"
    raw = path.read_bytes()
    # Convert PNG via Pillow if needed — Gemini accepts PNG; keep as-is
    result = analyze_image(raw, mime_type=mime)
    print(json.dumps(result.model_dump(), indent=2, ensure_ascii=False))

    nmi = (result.identifiers.nmi or "").replace(" ", "")
    fuse_amps = [f.rating_amps for f in result.protection.fuses if f.rating_amps]
    print("\n--- checks ---", file=sys.stderr)
    print(f"scene_type={result.scene_type}", file=sys.stderr)
    print(f"nmi={result.identifiers.nmi}", file=sys.stderr)
    print(f"fuse_amps={fuse_amps}", file=sys.stderr)
    print(f"components={len(result.components)}", file=sys.stderr)

    ok = True
    if "4310" not in nmi and "431091961" not in nmi:
        print("WARN: NMI may be incomplete", file=sys.stderr)
        ok = False
    if 80 not in fuse_amps:
        print("WARN: expected 80A fuse rating", file=sys.stderr)
        ok = False
    if not result.components:
        print("WARN: no components", file=sys.stderr)
        ok = False
    raise SystemExit(0 if ok else 2)


if __name__ == "__main__":
    main()
