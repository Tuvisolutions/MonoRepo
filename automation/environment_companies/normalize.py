"""Map a Places API place/details payload to one company JSON record."""

from __future__ import annotations

from typing import Any


def _localized_text(value: Any) -> str:
    if isinstance(value, str):
        return value.strip()
    if isinstance(value, dict):
        return str(value.get("text") or "").strip()
    return ""


def _hours(opening_hours: dict | None) -> list[str]:
    if not opening_hours:
        return []
    descriptions = opening_hours.get("weekdayDescriptions") or opening_hours.get("weekday_text") or []
    return [str(line).strip() for line in descriptions if str(line).strip()]


def company_from_place(place: dict, *, city: str, query: str = "") -> dict[str, Any]:
    location = place.get("location") or {}
    place_id = str(place.get("id") or "").strip()
    if place_id.startswith("places/"):
        place_id = place_id.split("/", 1)[1]
    types = [str(item) for item in (place.get("types") or []) if item]
    status = str(place.get("businessStatus") or "").strip() or "UNKNOWN"
    return {
        "place_id": place_id,
        "name": _localized_text(place.get("displayName")),
        "primary_type": str(place.get("primaryType") or "").strip(),
        "types": types,
        "formatted_address": str(place.get("formattedAddress") or "").strip(),
        "city": city.split(",")[0].strip(),
        "lat": location.get("latitude"),
        "lng": location.get("longitude"),
        "phone": str(
            place.get("nationalPhoneNumber")
            or place.get("internationalPhoneNumber")
            or ""
        ).strip(),
        "website": str(place.get("websiteUri") or "").strip(),
        "google_maps_uri": str(place.get("googleMapsUri") or "").strip(),
        "rating": place.get("rating"),
        "review_count": place.get("userRatingCount") or 0,
        "business_status": status,
        "editorial_summary": _localized_text(place.get("editorialSummary")),
        "hours": _hours(place.get("regularOpeningHours")),
        "matched_query": query,
    }
