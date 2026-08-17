#!/usr/bin/env python3
"""Fetch electrical EcoAudit photos + metadata from Sustainability Wise.

Sources (pick one):
  1. DATABASE_URL + Spaces credentials (local or after copying /opt/sw-api/.env)
  2. --ssh-prod  (run query+download on the production VM, then scp tarball home)

Outputs:
  <out>/images/<photo_id>.jpg
  <out>/manifest.jsonl   # one row per photo with metadata
  <out>/metadata.json    # summary counts

Example:
  python scripts/fetch_sw_electrical_photos.py --limit 100 --prefer-nmi \\
    --env-file /opt/sw-api/.env --out dataset/sw_prod
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Optional

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

from sw_common import (  # noqa: E402
    ELECTRICAL_ENTITY_TYPES,
    PREFERRED_FIELDS,
    caption_from_photo_descs,
    extension_for,
    load_dotenv_file,
    normalize_nmi,
    write_jsonl,
)


def connect_db():
    import psycopg

    url = (os.getenv("DATABASE_URL") or "").strip()
    if not url:
        raise SystemExit("DATABASE_URL missing (pass --env-file pointing at SW .env)")
    # Managed DigitalOcean PG needs SSL.
    kwargs: dict[str, Any] = {"connect_timeout": 30}
    if "sslmode=" not in url and "ondigitalocean.com" in url:
        kwargs["sslmode"] = "require"
    return psycopg.connect(url, **kwargs)


def spaces_client():
    import boto3
    from botocore.client import Config

    endpoint = (os.getenv("SPACES_ENDPOINT") or "").rstrip("/")
    region = os.getenv("SPACES_REGION") or "syd1"
    key = os.getenv("SPACES_ACCESS_KEY_ID") or ""
    secret = os.getenv("SPACES_SECRET_ACCESS_KEY") or ""
    bucket = os.getenv("SPACES_BUCKET") or ""
    if not all([endpoint, key, secret, bucket]):
        raise SystemExit(
            "Spaces credentials missing (SPACES_ENDPOINT/BUCKET/ACCESS_KEY_ID/SECRET_ACCESS_KEY)"
        )
    client = boto3.client(
        "s3",
        region_name=region,
        endpoint_url=endpoint,
        aws_access_key_id=key,
        aws_secret_access_key=secret,
        config=Config(signature_version="s3v4"),
    )
    return client, bucket


SQL = """
SELECT
  pr.id,
  pr.storage_key,
  pr.entity_type,
  pr.entity_id,
  pr.field_name,
  pr.original_filename,
  pr.content_type,
  pr.parent_id,
  pr.checksum,
  pr.file_size_bytes,
  pr.created_at,
  msb.site_nmi AS site_nmi,
  COALESCE(msb.name, asb.name, sp.inverter_brand_model, ge.question) AS entity_name,
  COALESCE(msb.photo_descs, asb.photo_descs, sp.photo_descs, ge.photo_descs) AS photo_descs,
  a.site_name AS audit_site_name
FROM photo_registry pr
LEFT JOIN ea_audits a
  ON a.id = pr.parent_id
LEFT JOIN ea_main_switchboards msb
  ON pr.entity_type = 'main_switchboard' AND pr.entity_id = msb.id::text
LEFT JOIN ea_additional_switchboards asb
  ON pr.entity_type = 'additional_switchboard' AND pr.entity_id = asb.id::text
LEFT JOIN ea_solar_pv sp
  ON pr.entity_type = 'solar_pv' AND pr.entity_id = sp.id::text
LEFT JOIN ea_general_electricity ge
  ON pr.entity_type = 'general_electricity' AND pr.entity_id = ge.id::text
WHERE pr.app = 'ecoaudit'
  AND pr.status = 'confirmed'
  AND pr.storage_key IS NOT NULL
  AND pr.entity_type = ANY(%s)
ORDER BY
  CASE
    WHEN msb.site_nmi IS NOT NULL AND trim(msb.site_nmi) <> '' THEN 0
    ELSE 1
  END,
  CASE pr.field_name
    WHEN 'photo' THEN 0
    WHEN 'electricityMeterPhoto' THEN 1
    WHEN 'switchboardPhoto' THEN 2
    WHEN 'inverterLabelPhoto' THEN 3
    WHEN 'photos[0]' THEN 4
    ELSE 10
  END,
  pr.created_at DESC NULLS LAST
"""


def fetch_rows(
    *,
    limit: int,
    prefer_nmi: bool,
    entity_types: tuple[str, ...],
    field_names: Optional[list[str]],
) -> list[dict[str, Any]]:
    with connect_db() as conn:
        with conn.cursor() as cur:
            cur.execute(SQL, (list(entity_types),))
            cols = [d.name for d in cur.description]
            rows = [dict(zip(cols, row)) for row in cur.fetchall()]

    if field_names:
        allowed = set(field_names)
        rows = [r for r in rows if r.get("field_name") in allowed]
    if prefer_nmi:
        with_nmi = [r for r in rows if normalize_nmi(r.get("site_nmi"))]
        without = [r for r in rows if not normalize_nmi(r.get("site_nmi"))]
        rows = with_nmi + without
    return rows[:limit]


def download_row(client, bucket: str, row: dict[str, Any], images_dir: Path) -> dict[str, Any]:
    photo_id = str(row["id"])
    storage_key = row["storage_key"]
    ext = extension_for(row.get("content_type"), storage_key, row.get("original_filename"))
    dest = images_dir / f"{photo_id}{ext}"
    if not dest.exists():
        client.download_file(bucket, storage_key, str(dest))
    caption = caption_from_photo_descs(row.get("photo_descs"), row.get("field_name") or "")
    nmi = normalize_nmi(row.get("site_nmi"))
    return {
        "photo_id": photo_id,
        "image_relpath": f"images/{dest.name}",
        "storage_key": storage_key,
        "entity_type": row.get("entity_type"),
        "entity_id": row.get("entity_id"),
        "field_name": row.get("field_name"),
        "entity_name": row.get("entity_name"),
        "audit_site_name": row.get("audit_site_name"),
        "site_nmi": nmi,
        "site_nmi_raw": row.get("site_nmi"),
        "caption": caption,
        "original_filename": row.get("original_filename"),
        "content_type": row.get("content_type"),
        "checksum": row.get("checksum"),
        "file_size_bytes": row.get("file_size_bytes"),
        "parent_id": row.get("parent_id"),
    }


def run_local(args: argparse.Namespace) -> None:
    if args.env_file:
        load_dotenv_file(Path(args.env_file))
    # Also accept OCR poc .env for GEMINI later; Spaces still need SW env.
    load_dotenv_file(ROOT / ".env")

    out = Path(args.out)
    images_dir = out / "images"
    images_dir.mkdir(parents=True, exist_ok=True)

    entity_types = tuple(args.entity_types) if args.entity_types else ELECTRICAL_ENTITY_TYPES
    rows = fetch_rows(
        limit=args.limit,
        prefer_nmi=args.prefer_nmi,
        entity_types=entity_types,
        field_names=args.fields,
    )
    print(f"Selected {len(rows)} photo rows")

    client, bucket = spaces_client()
    manifest: list[dict[str, Any]] = []
    errors = 0
    for index, row in enumerate(rows, start=1):
        try:
            item = download_row(client, bucket, row, images_dir)
            manifest.append(item)
            if index % 10 == 0 or index == len(rows):
                print(f"  downloaded {index}/{len(rows)}")
        except Exception as exc:  # noqa: BLE001 — keep batch going
            errors += 1
            print(f"  FAIL {row.get('id')}: {exc}")

    write_jsonl(out / "manifest.jsonl", manifest)
    summary = {
        "count": len(manifest),
        "errors": errors,
        "with_nmi": sum(1 for m in manifest if m.get("site_nmi")),
        "entity_types": sorted({m.get("entity_type") for m in manifest}),
        "out": str(out),
    }
    (out / "metadata.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
    print(json.dumps(summary, indent=2))


def run_ssh_prod(args: argparse.Namespace) -> None:
    """Query+download on the production VM, then scp a tarball home."""
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    remote_dir = args.remote_dir.rstrip("/")
    host = args.ssh_host
    key = args.ssh_key
    limit = args.limit
    prefer = "--prefer-nmi" if args.prefer_nmi else ""
    fields = ""
    if args.fields:
        fields = "--fields " + " ".join(args.fields)

    # scp scripts to VM then run with prod .env.
    subprocess.check_call(
        [
            "scp",
            "-i",
            key,
            "-o",
            "IdentitiesOnly=yes",
            str(ROOT / "scripts" / "sw_common.py"),
            str(ROOT / "scripts" / "fetch_sw_electrical_photos.py"),
            f"root@{host}:/tmp/",
        ]
    )
    remote_cmd = (
        f"set -a; . /opt/sw-api/.env; set +a; "
        f"python3 -m venv /tmp/ocr-fetch-venv >/dev/null 2>&1 || true; "
        f"/tmp/ocr-fetch-venv/bin/pip install -q 'psycopg[binary]' boto3; "
        f"mkdir -p {remote_dir}; "
        f"cd /tmp && /tmp/ocr-fetch-venv/bin/python fetch_sw_electrical_photos.py "
        f"--limit {limit} {prefer} {fields} --out {remote_dir} && "
        f"tar -C {remote_dir} -czf /tmp/sw_electrical_photos.tgz ."
    )
    subprocess.check_call(
        [
            "ssh",
            "-i",
            key,
            "-o",
            "IdentitiesOnly=yes",
            f"root@{host}",
            remote_cmd,
        ]
    )
    tarball = out / "sw_electrical_photos.tgz"
    subprocess.check_call(
        [
            "scp",
            "-i",
            key,
            "-o",
            "IdentitiesOnly=yes",
            f"root@{host}:/tmp/sw_electrical_photos.tgz",
            str(tarball),
        ]
    )
    subprocess.check_call(["tar", "-xzf", str(tarball), "-C", str(out)])
    print(f"Fetched into {out}")
    meta = out / "metadata.json"
    if meta.is_file():
        print(meta.read_text(encoding="utf-8"))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=ROOT / "dataset" / "sw_prod")
    parser.add_argument("--limit", type=int, default=100)
    parser.add_argument("--prefer-nmi", action="store_true", help="Prioritize rows with site_nmi")
    parser.add_argument(
        "--entity-types",
        nargs="*",
        default=None,
        help=f"Default: {' '.join(ELECTRICAL_ENTITY_TYPES)}",
    )
    parser.add_argument(
        "--fields",
        nargs="*",
        default=None,
        help=f"Optional field filter, e.g. {' '.join(PREFERRED_FIELDS[:4])}",
    )
    parser.add_argument("--env-file", type=Path, default=None, help="SW .env with DB + Spaces")
    parser.add_argument(
        "--ssh-prod",
        action="store_true",
        help="Run fetch on production VM and scp results home",
    )
    parser.add_argument("--ssh-host", default="170.64.154.143")
    parser.add_argument("--ssh-key", default=str(Path.home() / ".ssh" / "id_ed25519"))
    parser.add_argument("--remote-dir", default="/tmp/ocr-electrical-sw-prod")
    args = parser.parse_args()

    if args.ssh_prod:
        run_ssh_prod(args)
    else:
        run_local(args)


if __name__ == "__main__":
    main()
