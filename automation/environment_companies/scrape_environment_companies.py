#!/usr/bin/env python3
"""Scrape Australian environmental companies via Google Places (Maps) Text Search.

Usage:
  python scrape_environment_companies.py --city Sydney --total 50
  python scrape_environment_companies.py --cities Sydney Melbourne --total 30
  python scrape_environment_companies.py --city Sydney --dry-run
"""

from __future__ import annotations

import argparse
import json
import logging
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from env_loader import load_project_env

load_project_env()

from normalize import company_from_place  # noqa: E402
from places_client import (  # noqa: E402
    PlacesAPIError,
    get_place_details,
    place_id_of,
    search_text_page,
)
from queries import (  # noqa: E402
    SEARCH_QUERIES,
    SUPPORTED_CITIES,
    city_key,
    get_city_search_bounds,
    is_off_niche,
)

log = logging.getLogger("env_scrape")
ROOT = Path(__file__).resolve().parent
OUTPUT_DIR = ROOT / "output"


def _parse_cities(city: str | None, cities: list[str] | None) -> list[str]:
    raw = list(cities or [])
    if city:
        raw.insert(0, city)
    if not raw:
        raise SystemExit("Pass --city or --cities")
    resolved: list[str] = []
    seen: set[str] = set()
    for item in raw:
        key = city_key(item)
        get_city_search_bounds(item)
        label = key.title()
        if key in seen:
            continue
        seen.add(key)
        resolved.append(label)
    return resolved


def _closed(status: str) -> bool:
    return status.upper() in {"CLOSED_PERMANENTLY", "CLOSED_TEMPORARILY"}


def scrape_city(city: str, total: int, *, pause: float = 0.25) -> dict:
    companies: list[dict] = []
    seen_ids: set[str] = set()
    per_query: dict[str, int] = {}

    for query in SEARCH_QUERIES:
        if len(companies) >= total:
            break
        added = 0
        page_token = ""
        while len(companies) < total:
            remaining = total - len(companies)
            page_size = min(20, remaining)
            try:
                places, page_token = search_text_page(
                    city=city,
                    query=query,
                    page_token=page_token,
                    page_size=page_size,
                )
            except PlacesAPIError as exc:
                log.error("Search failed for %s / %s: %s", city, query, exc)
                break
            if not places:
                break
            for place in places:
                if len(companies) >= total:
                    break
                place_id = place_id_of(place)
                if not place_id or place_id in seen_ids:
                    continue
                types = [str(item) for item in (place.get("types") or [])]
                if is_off_niche(types):
                    continue
                status = str(place.get("businessStatus") or "")
                if _closed(status):
                    continue
                try:
                    details = get_place_details(place_id)
                except PlacesAPIError as exc:
                    log.warning("Details failed for %s: %s", place_id, exc)
                    continue
                if not details:
                    continue
                if _closed(str(details.get("businessStatus") or status)):
                    continue
                detail_types = [str(item) for item in (details.get("types") or types)]
                if is_off_niche(detail_types):
                    continue
                record = company_from_place(details, city=city, query=query)
                if not record.get("name") or not record.get("place_id"):
                    continue
                seen_ids.add(place_id)
                companies.append(record)
                added += 1
                time.sleep(pause)
            if not page_token:
                break
            time.sleep(pause)
        per_query[query] = added
        log.info("%s / %s: +%d (total %d)", city, query, added, len(companies))

    return {
        "meta": {
            "version": "1.0",
            "scraped_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "source": "google_places_api_new",
            "niche": "environment",
            "city": city,
            "queries": list(SEARCH_QUERIES),
            "requested": total,
            "count": len(companies),
            "added_per_query": per_query,
        },
        "companies": companies,
    }


def write_document(document: dict, city: str) -> Path:
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    path = OUTPUT_DIR / f"environment_companies_{city_key(city)}_{stamp}.json"
    path.write_text(json.dumps(document, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--city", help="One Australian city (Sydney, Melbourne, Perth, Adelaide, Brisbane)")
    parser.add_argument("--cities", nargs="*", help="Multiple cities")
    parser.add_argument("--total", type=int, default=50, help="Max unique companies per city")
    parser.add_argument("--dry-run", action="store_true", help="Print the query plan without API calls")
    parser.add_argument("--pause", type=float, default=0.25, help="Seconds between Places requests")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    cities = _parse_cities(args.city, args.cities)
    if args.total < 1:
        raise SystemExit("--total must be >= 1")

    if args.dry_run:
        plan = {
            "cities": cities,
            "queries": list(SEARCH_QUERIES),
            "total_per_city": args.total,
            "source": "google_places_api_new",
        }
        print(json.dumps(plan, indent=2))
        return

    written: list[str] = []
    for city in cities:
        document = scrape_city(city, args.total, pause=args.pause)
        path = write_document(document, city)
        written.append(str(path))
        log.info("Wrote %d companies → %s", document["meta"]["count"], path)

    print(json.dumps({"files": written}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
