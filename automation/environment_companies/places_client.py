"""Google Places API (New) Text Search + Place Details for environment companies."""

from __future__ import annotations

import logging
import time
from typing import Any
from urllib.parse import quote

import requests

from env_loader import get_places_api_key
from queries import get_city_search_bounds

log = logging.getLogger("env_places")

PLACES_V1_BASE = "https://places.googleapis.com/v1"

DISCOVERY_FIELD_MASK = ",".join(
    [
        "places.id",
        "places.displayName",
        "places.formattedAddress",
        "places.location",
        "places.primaryType",
        "places.types",
        "places.businessStatus",
        "nextPageToken",
    ]
)

DETAIL_FIELD_MASK = ",".join(
    [
        "id",
        "displayName",
        "formattedAddress",
        "addressComponents",
        "nationalPhoneNumber",
        "internationalPhoneNumber",
        "websiteUri",
        "googleMapsUri",
        "rating",
        "userRatingCount",
        "types",
        "primaryType",
        "location",
        "regularOpeningHours",
        "editorialSummary",
        "businessStatus",
    ]
)


class PlacesAPIError(ValueError):
    def __init__(self, message: str, *, status_code: int) -> None:
        super().__init__(message)
        self.status_code = status_code


def _localized_text(value: Any) -> str:
    if isinstance(value, str):
        return value.strip()
    if isinstance(value, dict):
        return str(value.get("text") or "").strip()
    return ""


def _request(
    method: str,
    path: str,
    *,
    field_mask: str,
    label: str,
    body: dict | None = None,
    timeout: int = 25,
    attempts: int = 3,
) -> dict:
    api_key = get_places_api_key()
    url = f"{PLACES_V1_BASE}/{path.lstrip('/')}"
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "X-Goog-Api-Key": api_key,
        "X-Goog-FieldMask": field_mask,
    }

    last_error: Exception | None = None
    for attempt in range(1, attempts + 1):
        try:
            response = requests.request(
                method,
                url,
                headers=headers,
                json=body,
                timeout=timeout,
            )
            if response.status_code < 200 or response.status_code >= 300:
                message = f"Places API HTTP {response.status_code}: {response.text[:300]}"
                if response.status_code != 429 and response.status_code < 500:
                    raise PlacesAPIError(message, status_code=response.status_code)
                raise RuntimeError(message)
            if not response.content:
                return {}
            return response.json()
        except PlacesAPIError:
            raise
        except (requests.RequestException, RuntimeError) as exc:
            last_error = exc
            if attempt >= attempts:
                raise
            wait = 1.5 ** (attempt - 1)
            log.warning(
                "[%s] attempt %d/%d failed: %s — retry in %.1fs",
                label,
                attempt,
                attempts,
                exc,
                wait,
            )
            time.sleep(wait)
    if last_error:
        raise last_error
    return {}


def search_text_page(
    *,
    city: str,
    query: str,
    page_token: str = "",
    page_size: int = 20,
) -> tuple[list[dict], str]:
    if page_size < 1 or page_size > 20:
        raise ValueError("page_size must be between 1 and 20")
    bounds = get_city_search_bounds(city)
    city_label = city.split(",")[0].strip()
    body: dict[str, Any] = {
        "textQuery": f"{query} in {city_label}, Australia",
        "locationRestriction": {
            "rectangle": {
                "low": {
                    "latitude": bounds["low_lat"],
                    "longitude": bounds["low_lng"],
                },
                "high": {
                    "latitude": bounds["high_lat"],
                    "longitude": bounds["high_lng"],
                },
            }
        },
        "languageCode": "en",
        "regionCode": "AU",
        "pageSize": page_size,
    }
    if page_token:
        body["pageToken"] = page_token

    data = _request(
        "POST",
        "places:searchText",
        field_mask=DISCOVERY_FIELD_MASK,
        label=f"search:{city_label}:{query}",
        body=body,
    )
    places = list(data.get("places") or [])
    return places, str(data.get("nextPageToken") or "").strip()


def get_place_details(place_id: str) -> dict:
    place_id = place_id.strip()
    if not place_id:
        return {}
    return _request(
        "GET",
        f"places/{quote(place_id, safe='')}",
        field_mask=DETAIL_FIELD_MASK,
        label=f"details:{place_id[:12]}",
    )


def localized_name(place: dict) -> str:
    return _localized_text(place.get("displayName"))


def place_id_of(place: dict) -> str:
    raw = str(place.get("id") or "").strip()
    if raw.startswith("places/"):
        return raw.split("/", 1)[1]
    return raw
