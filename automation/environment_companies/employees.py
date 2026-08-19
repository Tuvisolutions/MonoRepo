"""Parse up to N named people from public Team/About pages."""

from __future__ import annotations

import json
import re
import time
from urllib.parse import unquote, urljoin, urlsplit

from bs4 import BeautifulSoup

from website_fetch import fetch_public_html, normalize_website, same_host

EMAIL_RE = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
NAME_CAPTURE = re.compile(
    r"\b([A-Z][a-z]+(?:[-'][A-Z][a-z]+)?(?:\s+[A-Z][a-z]+(?:[-'][A-Z][a-z]+)?){1,2})\b"
)
ROLE_RE = re.compile(
    r"\b(managing director|executive director|director|principal consultant|"
    r"principal|founder|co-founder|consultant|engineer|manager|scientist|"
    r"partner|associate|team lead|head of|chief executive|ceo|cto|ecologist|"
    r"hygienist|project manager)\b",
    re.I,
)
NAME_STOP = {
    "about",
    "aerial",
    "air",
    "airport",
    "alignment",
    "america",
        "assessment",
        "assessments",
    "associate",
    "auditors",
    "australia",
    "background",
    "better",
    "book",
    "civil",
    "classification",
    "click",
        "company",
        "consultancy",
    "consultant",
    "consultants",
    "consulting",
    "contact",
    "contaminated",
    "cookie",
    "director",
    "drop",
    "drones",
    "emergency",
    "engineer",
    "environments",
    "environmental",
    "equipment",
    "expertise",
    "experts",
    "fitness",
    "general",
    "geotechnical",
    "greater",
    "group",
    "hazardous",
    "hazmat",
    "high",
    "home",
    "hygiene",
    "indoor",
    "industry",
    "initiatives",
    "inspections",
    "investigation",
    "jannali",
    "laboratory",
    "leading",
    "learn",
    "light",
    "manager",
    "material",
        "management",
        "meet",
    "metro",
    "mobile",
    "monitoring",
    "more",
    "north",
    "occupational",
    "off",
    "online",
    "our",
    "page",
    "parramatta",
    "pioneering",
    "principal",
    "privacy",
    "projects",
    "quality",
    "quick",
    "rail",
    "read",
    "recent",
    "response",
        "risk",
        "sample",
    "sampling",
    "search",
    "service",
    "services",
    "site",
    "skip",
    "social",
    "soil",
    "solutions",
    "south",
    "station",
    "surveys",
    "sustainability",
    "sydney",
    "team",
    "technical",
    "terms",
    "testing",
    "the",
    "this",
    "truly",
    "upgrade",
    "us",
    "vehicles",
    "waste",
    "western",
    "work",
    "works",
    "your",
}
PAGE_MARKERS = (
    "team",
    "about",
    "people",
    "staff",
    "our-team",
    "our_team",
    "leadership",
    "who-we-are",
    "who_we_are",
    "directors",
)
COMMON_PATHS = ("/team", "/our-team", "/people", "/about", "/about-us", "/staff", "/leadership")
MAX_PAGES = 4


def _clean_space(value: str) -> str:
    return re.sub(r"\s+", " ", (value or "").strip())


def _is_person_name(name: str) -> bool:
    name = _clean_space(name)
    if not NAME_CAPTURE.fullmatch(name):
        return False
    words = name.split()
    for word in words:
        parts = re.split(r"[-']", word.lower())
        if any(part in NAME_STOP for part in parts if part):
            return False
    if any(len(word) < 2 for word in words):
        return False
    return True


def _short_title(text: str) -> str:
    text = _clean_space(text)
    if not text:
        return ""
    if ROLE_RE.search(text) and len(text.split()) <= 8:
        return text[:80]
    return ""


def _pairs_from_lines(text: str, page_url: str) -> list[dict[str, str]]:
    lines = [_clean_space(line) for line in (text or "").split("\n")]
    lines = [line for line in lines if line]
    people: list[dict[str, str]] = []
    for index, line in enumerate(lines[:-1]):
        if not _is_person_name(line):
            continue
        title_line = lines[index + 1]
        if len(title_line.split()) > 10:
            continue
        title = _short_title(title_line)
        if not title:
            continue
        people.append({"name": line, "title": title, "email": "", "source_url": page_url})
    return people


def _emails_from_text(text: str) -> list[str]:
    found: list[str] = []
    for raw in EMAIL_RE.findall(text or ""):
        email = raw.rstrip(".").lower()
        if any(bad in email for bad in ("example.com", "wixpress", "sentry", "schema.org", "placeholder")):
            continue
        found.append(email)
    return list(dict.fromkeys(found))


def _person_from_jsonld(payload: object) -> list[dict[str, str]]:
    people: list[dict[str, str]] = []

    def walk(node: object) -> None:
        if isinstance(node, list):
            for item in node:
                walk(item)
            return
        if not isinstance(node, dict):
            return
        graph = node.get("@graph")
        if graph:
            walk(graph)
        type_value = node.get("@type") or node.get("type") or ""
        types = type_value if isinstance(type_value, list) else [type_value]
        types = [str(item).lower() for item in types]
        if "person" in types:
            name = _clean_space(str(node.get("name") or ""))
            title = _clean_space(str(node.get("jobTitle") or node.get("title") or ""))
            email = ""
            raw_email = node.get("email")
            if isinstance(raw_email, str):
                email = raw_email.replace("mailto:", "").strip()
            if name and _is_person_name(name):
                people.append({"name": name, "title": title, "email": email})
        for value in node.values():
            if isinstance(value, (dict, list)):
                walk(value)

    walk(payload)
    return people


def _extract_from_html(html: str, page_url: str, *, people_page: bool) -> list[dict[str, str]]:
    soup = BeautifulSoup(html, "html.parser")
    for tag in soup.find_all(["style", "noscript", "svg"]):
        tag.decompose()
    for tag in soup.find_all("script"):
        if str(tag.get("type") or "").lower() != "application/ld+json":
            tag.decompose()

    people: list[dict[str, str]] = []
    for script in soup.find_all("script", type="application/ld+json"):
        raw = script.string or script.get_text() or ""
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            continue
        people.extend(_person_from_jsonld(payload))

    mailto_by_name: dict[str, str] = {}
    for link in soup.find_all("a", href=True):
        href = str(link.get("href") or "")
        if href.lower().startswith("mailto:"):
            email = unquote(href.split(":", 1)[1]).split("?")[0].strip()
            label = _clean_space(link.get_text(" ", strip=True))
            if EMAIL_RE.fullmatch(email) and label and _is_person_name(label):
                mailto_by_name[label.lower()] = email.lower()
                people.append({"name": label, "title": "", "email": email.lower()})

    if people_page:
        visible = soup.get_text("\n", strip=True)
        emails = _emails_from_text(visible)
        people.extend(_pairs_from_lines(visible, page_url))
        for heading in soup.find_all(["h1", "h2", "h3", "h4", "strong"]):
            name = _clean_space(heading.get_text(" ", strip=True))
            if not _is_person_name(name):
                continue
            nxt = heading.find_next(["h3", "h4", "h5", "h6", "p", "span"])
            sibling_text = _clean_space(nxt.get_text(" ", strip=True) if nxt else "")
            if len(sibling_text.split()) > 10:
                continue
            title = _short_title(sibling_text)
            if not title:
                continue
            people.append(
                {
                    "name": name,
                    "title": title,
                    "email": mailto_by_name.get(name.lower(), ""),
                }
            )
        for person in people:
            if person.get("name") and not person.get("email"):
                name = person["name"]
                person["email"] = next(
                    (
                        item
                        for item in emails
                        if item.split("@")[0][:4].lower() in name.lower().replace(" ", "")
                    ),
                    person.get("email") or "",
                )

    merged: dict[str, dict[str, str]] = {}
    for person in people:
        name = _clean_space(person.get("name") or "")
        if not name or not _is_person_name(name):
            continue
        key = name.lower()
        current = merged.setdefault(
            key,
            {"name": name, "title": "", "email": "", "source_url": page_url},
        )
        if person.get("title") and not current["title"]:
            current["title"] = _clean_space(person["title"])
        if person.get("email") and not current["email"]:
            current["email"] = person["email"].strip().lower()
        if person.get("source_url"):
            current["source_url"] = person["source_url"]
    return list(merged.values())


SKIP_PATH = ("contact", "career", "careers", "job", "jobs", "blog", "news", "privacy", "cookie")


def _looks_like_people_page(url: str) -> bool:
    path = urlsplit(url).path.lower()
    if any(token in path for token in SKIP_PATH):
        return False
    return any(marker in path for marker in PAGE_MARKERS)


def discover_people_pages(home_html: str, home_url: str) -> list[str]:
    soup = BeautifulSoup(home_html, "html.parser")
    found: list[str] = []

    def consider(absolute: str) -> None:
        path = urlsplit(absolute).path.lower()
        if any(token in path for token in SKIP_PATH):
            return
        if absolute.rstrip("/") == home_url.rstrip("/"):
            return
        if absolute not in found:
            found.append(absolute)

    for link in soup.find_all("a", href=True):
        href = str(link.get("href") or "").strip()
        label = f"{href} {link.get_text(' ', strip=True)}".lower()
        if not any(marker in label for marker in PAGE_MARKERS):
            continue
        absolute = urljoin(home_url, href)
        if not same_host(home_url, absolute):
            continue
        consider(absolute)
        if len(found) >= MAX_PAGES - 1:
            break
    if len(found) < 2:
        parsed_home = home_url.rstrip("/")
        for path in COMMON_PATHS:
            candidate = urljoin(parsed_home + "/", path.lstrip("/"))
            consider(candidate)
            if len(found) >= MAX_PAGES - 1:
                break
    found.sort(
        key=lambda url: 0 if any(token in urlsplit(url).path.lower() for token in ("team", "people", "staff", "leadership")) else 1
    )
    return found[: MAX_PAGES - 1]


def scrape_employees_from_website(
    website: str,
    *,
    max_employees: int = 10,
    timeout: int = 20,
    pause: float = 0.3,
) -> tuple[list[dict[str, str]], dict[str, object]]:
    start = normalize_website(website)
    stats: dict[str, object] = {"pages_fetched": 0, "found": 0, "error": ""}
    if not start:
        stats["error"] = "no_website"
        return [], stats

    html, final_url = fetch_public_html(start, timeout=timeout)
    if not html:
        stats["error"] = "homepage_fetch_failed"
        return [], stats
    stats["pages_fetched"] = 1

    pages = [final_url]
    for extra in discover_people_pages(html, final_url):
        if extra not in pages:
            pages.append(extra)
        if len(pages) >= MAX_PAGES:
            break

    collected: list[dict[str, str]] = []
    seen: set[str] = set()
    extra_pages = [url for url in pages if url.rstrip("/") != final_url.rstrip("/")]
    ordered = extra_pages + [final_url]
    for url in ordered:
        if url.rstrip("/") == final_url.rstrip("/"):
            page_html, page_url = html, final_url
            people_page = _looks_like_people_page(page_url)
        else:
            time.sleep(pause)
            page_html, page_url = fetch_public_html(url, timeout=timeout)
            stats["pages_fetched"] = int(stats["pages_fetched"]) + 1
            if not page_html:
                continue
            people_page = _looks_like_people_page(page_url) or _looks_like_people_page(url)
        for person in _extract_from_html(page_html, page_url, people_page=people_page):
            key = person["name"].lower()
            if key in seen:
                continue
            seen.add(key)
            collected.append(person)

    ranked = sorted(
        collected,
        key=lambda person: (bool(person.get("email")), bool(person.get("title"))),
        reverse=True,
    )
    kept = ranked[:max_employees]
    stats["found"] = len(kept)
    return kept, stats
