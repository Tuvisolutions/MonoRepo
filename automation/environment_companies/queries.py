"""Environment-niche search phrases and off-niche type filters."""

from __future__ import annotations

SEARCH_QUERIES = (
    "environmental consultant",
    "sustainability consultant",
    "solar installer",
    "waste management",
    "recycling",
    "energy efficiency consultant",
    "environmental engineering",
)

CITY_SEARCH_BOUNDS: dict[str, dict[str, float]] = {
    "sydney": {"low_lat": -34.15, "low_lng": 150.55, "high_lat": -33.55, "high_lng": 151.40},
    "melbourne": {"low_lat": -38.15, "low_lng": 144.45, "high_lat": -37.45, "high_lng": 145.65},
    "perth": {"low_lat": -32.30, "low_lng": 115.55, "high_lat": -31.55, "high_lng": 116.20},
    "adelaide": {"low_lat": -35.20, "low_lng": 138.30, "high_lat": -34.55, "high_lng": 139.00},
    "brisbane": {"low_lat": -27.80, "low_lng": 152.70, "high_lat": -27.10, "high_lng": 153.45},
}

SUPPORTED_CITIES = tuple(name.title() for name in CITY_SEARCH_BOUNDS)

# Drop obvious Maps noise that is not an environmental services company.
TYPE_DENYLIST = {
    "restaurant",
    "meal_takeaway",
    "meal_delivery",
    "cafe",
    "bar",
    "lodging",
    "hotel",
    "supermarket",
    "grocery_store",
    "gas_station",
    "school",
    "university",
    "hospital",
    "pharmacy",
    "church",
    "park",
    "tourist_attraction",
}


def city_key(city: str) -> str:
    return city.split(",")[0].strip().lower().replace("_", " ")


def get_city_search_bounds(city: str) -> dict[str, float]:
    key = city_key(city)
    bounds = CITY_SEARCH_BOUNDS.get(key)
    if bounds is None:
        supported = ", ".join(SUPPORTED_CITIES)
        raise ValueError(f"No search bounds for {city!r}; supported: {supported}")
    return dict(bounds)


def is_off_niche(types: list[str]) -> bool:
    lowered = {str(item).strip().lower() for item in types if item}
    return bool(lowered & TYPE_DENYLIST)
