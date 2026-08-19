"""Load Places API key without overriding an already-set process environment."""

from __future__ import annotations

import os
from pathlib import Path

from dotenv import dotenv_values

_HERE = Path(__file__).resolve().parent
_MONOREPO = _HERE.parents[1]
_OUTREACH = _MONOREPO / "automation" / "outreach"


def load_project_env() -> None:
    paths = [
        _MONOREPO / ".env",
        _MONOREPO / "backend" / ".env",
        _OUTREACH / ".env",
        _HERE / ".env",
    ]
    configured = os.getenv("INGESTION_ENV_FILE", "").strip()
    if configured:
        paths.append(Path(configured).expanduser())

    merged: dict[str, str] = {}
    for path in paths:
        if not path.is_file():
            continue
        for key, value in dotenv_values(path).items():
            if value is not None:
                merged[key] = value

    for key, value in merged.items():
        os.environ.setdefault(key, value)


def get_places_api_key() -> str:
    key = (
        os.getenv("PLACES_API", "").strip()
        or os.getenv("GOOGLE_PLACES_API_KEY", "").strip()
    )
    if not key:
        raise ValueError(
            "PLACES_API or GOOGLE_PLACES_API_KEY is missing. "
            "Copy .env.example to .env or use the outreach/backend env file."
        )
    return key
