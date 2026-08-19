"""SSRF-safe public HTML fetch (same-host, no private IPs, size-capped)."""

from __future__ import annotations

import ipaddress
import socket
from urllib.parse import parse_qsl, urlencode, urljoin, urlsplit, urlunsplit

import requests

MAX_WEBSITE_BYTES = 1_000_000
USER_AGENT = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
)


def is_public_http_url(raw_url: str) -> bool:
    try:
        parsed = urlsplit(raw_url)
    except ValueError:
        return False
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        return False
    if parsed.username or parsed.password:
        return False
    try:
        addresses = socket.getaddrinfo(
            parsed.hostname,
            parsed.port or (443 if parsed.scheme == "https" else 80),
        )
    except socket.gaierror:
        return False
    for address in addresses:
        try:
            ip = ipaddress.ip_address(address[4][0])
        except ValueError:
            return False
        if not ip.is_global:
            return False
    return True


def same_host(left: str, right: str) -> bool:
    def host(url: str) -> str:
        return (urlsplit(url).hostname or "").lower().removeprefix("www.")

    return bool(host(left) and host(left) == host(right))


def normalize_website(raw: str) -> str:
    website = (raw or "").strip()
    if not website:
        return ""
    if not website.startswith(("http://", "https://")):
        website = f"https://{website}"
    parsed = urlsplit(website)
    query = [
        (key, value)
        for key, value in parse_qsl(parsed.query, keep_blank_values=True)
        if not key.lower().startswith("utm_")
    ]
    return urlunsplit(
        (parsed.scheme, parsed.netloc, parsed.path or "/", urlencode(query), "")
    )


def fetch_public_html(raw_url: str, *, timeout: int = 20) -> tuple[str, str]:
    current = raw_url
    for _ in range(4):
        if not is_public_http_url(current):
            return "", ""
        response = requests.get(
            current,
            timeout=timeout,
            headers={"User-Agent": USER_AGENT, "Accept": "text/html,application/xhtml+xml"},
            allow_redirects=False,
            stream=True,
        )
        if response.status_code in (301, 302, 303, 307, 308):
            destination = response.headers.get("Location") or ""
            response.close()
            if not destination:
                return "", ""
            current = urljoin(current, destination)
            continue
        if response.status_code != 200:
            response.close()
            return "", ""
        content_type = (response.headers.get("Content-Type") or "").lower()
        if content_type and "html" not in content_type and not content_type.startswith("text/"):
            response.close()
            return "", ""
        chunks: list[bytes] = []
        total = 0
        for chunk in response.iter_content(chunk_size=16_384):
            if not chunk:
                continue
            remaining = MAX_WEBSITE_BYTES - total
            if remaining <= 0:
                break
            chunks.append(chunk[:remaining])
            total += min(len(chunk), remaining)
        encoding = response.encoding or "utf-8"
        response.close()
        return b"".join(chunks).decode(encoding, errors="replace"), current
    return "", ""
