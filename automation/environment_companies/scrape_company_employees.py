#!/usr/bin/env python3
"""Enrich environment-company JSON with up to 10 public website employees.

Usage:
  python scrape_company_employees.py --input output/environment_companies_sydney_....json
  python scrape_company_employees.py --latest --max-employees 10
"""

from __future__ import annotations

import argparse
import json
import logging
import sys
import time
from copy import deepcopy
from datetime import datetime, timezone
from pathlib import Path

from employees import scrape_employees_from_website

log = logging.getLogger("env_employees")
ROOT = Path(__file__).resolve().parent
OUTPUT_DIR = ROOT / "output"


def _latest_input() -> Path:
    candidates = [
        path
        for path in OUTPUT_DIR.glob("environment_companies_*.json")
        if "_employees" not in path.name and path.name != ".gitkeep"
    ]
    if not candidates:
        raise SystemExit(f"No company JSON files in {OUTPUT_DIR}")
    return max(candidates, key=lambda path: path.stat().st_mtime)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, help="Places company JSON from scrape_environment_companies.py")
    parser.add_argument("--latest", action="store_true", help="Use newest non-employee dump in output/")
    parser.add_argument("--max-employees", type=int, default=10)
    parser.add_argument("--pause", type=float, default=0.3)
    parser.add_argument("--limit", type=int, default=0, help="Only process the first N companies (0 = all)")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    if args.max_employees < 1:
        raise SystemExit("--max-employees must be >= 1")
    source = args.input or (_latest_input() if args.latest else None)
    if source is None:
        raise SystemExit("Pass --input or --latest")
    source = source.resolve()
    if not source.is_file():
        raise SystemExit(f"Missing input file: {source}")

    document = json.loads(source.read_text(encoding="utf-8"))
    companies = list(document.get("companies") or [])
    if args.limit:
        companies = companies[: args.limit]

    enriched: list[dict] = []
    for index, company in enumerate(companies, start=1):
        row = deepcopy(company)
        website = str(row.get("website") or "")
        name = str(row.get("name") or "unknown")
        try:
            employees, stats = scrape_employees_from_website(
                website,
                max_employees=args.max_employees,
                pause=args.pause,
            )
        except Exception as exc:  # noqa: BLE001 — keep batch going
            employees, stats = [], {
                "pages_fetched": 0,
                "found": 0,
                "error": str(exc)[:200],
            }
        row["employees"] = employees
        row["employee_scrape"] = stats
        enriched.append(row)
        log.info(
            "[%d/%d] %s: %s employees (%s)",
            index,
            len(companies),
            name,
            stats.get("found", 0),
            stats.get("error") or "ok",
        )
        time.sleep(args.pause)

    meta = dict(document.get("meta") or {})
    meta["employee_source"] = "company_website"
    meta["max_employees"] = args.max_employees
    meta["employee_scraped_at"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    meta["employee_input"] = str(source.name)
    out_doc = {"meta": meta, "companies": enriched}

    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    city = str(meta.get("city") or "unknown").lower().replace(" ", "_")
    dest = OUTPUT_DIR / f"environment_companies_{city}_{stamp}_employees.json"
    dest.write_text(json.dumps(out_doc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(json.dumps({"file": str(dest), "companies": len(enriched)}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
