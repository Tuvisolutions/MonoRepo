"""Shared helpers for Sustainability Wise prod photo fetch / label build."""

from __future__ import annotations

import json
import os
import re
from pathlib import Path
from typing import Any, Iterable, Optional

ELECTRICAL_ENTITY_TYPES = (
    "main_switchboard",
    "additional_switchboard",
    "solar_pv",
    "general_electricity",
)

# Prefer primary board / meter shots before huge extraPhotos dumps.
PREFERRED_FIELDS = (
    "photo",
    "electricityMeterPhoto",
    "switchboardPhoto",
    "inverterLabelPhoto",
    "roofPhoto",
    "photos[0]",
)


def load_dotenv_file(path: Path) -> None:
    """Load KEY=VALUE lines into os.environ without overriding existing values."""
    if not path.is_file():
        return
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip("'").strip('"')
        if key and key not in os.environ:
            os.environ[key] = value


def normalize_nmi(value: Optional[str]) -> Optional[str]:
    if not value:
        return None
    digits = re.sub(r"\D+", "", value)
    if not digits:
        cleaned = value.strip()
        return cleaned or None
    # Ignore placeholder / non-NMI codes that are not mostly digits.
    if len(digits) < 8:
        return value.strip() or None
    return digits


def scene_type_for_entity(entity_type: str, field_name: str) -> str:
    field = (field_name or "").lower()
    if "meter" in field:
        return "electrical_meter_board"
    if entity_type in {"main_switchboard", "additional_switchboard", "general_electricity"}:
        return "electrical_meter_board"
    if entity_type == "solar_pv":
        return "mixed"
    return "unknown"


def caption_from_photo_descs(photo_descs: Any, field_name: str) -> Optional[str]:
    if not isinstance(photo_descs, dict):
        return None
    keys = [field_name]
    # photoDescs often keyed as photo / extraPhotos.0
    m = re.match(r"^(extraPhotos|photos)\[(\d+)\]$", field_name or "")
    if m:
        keys.append(f"{m.group(1)}.{m.group(2)}")
    for key in keys:
        entry = photo_descs.get(key)
        if isinstance(entry, dict):
            name = entry.get("name")
            if isinstance(name, str) and name.strip():
                return name.strip()
        elif isinstance(entry, str) and entry.strip():
            return entry.strip()
    return None


def extension_for(content_type: Optional[str], storage_key: str, original_filename: Optional[str]) -> str:
    for candidate in (original_filename or "", storage_key or ""):
        suffix = Path(candidate).suffix.lower()
        if suffix in {".jpg", ".jpeg", ".png", ".webp", ".heic", ".tif", ".tiff"}:
            return ".jpg" if suffix == ".jpeg" else suffix
    ct = (content_type or "").lower()
    if "png" in ct:
        return ".png"
    if "webp" in ct:
        return ".webp"
    return ".jpg"


def write_jsonl(path: Path, rows: Iterable[dict[str, Any]]) -> int:
    path.parent.mkdir(parents=True, exist_ok=True)
    count = 0
    with path.open("w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False, default=str) + "\n")
            count += 1
    return count


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    with path.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            rows.append(json.loads(line))
    return rows
