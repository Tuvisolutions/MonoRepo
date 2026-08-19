# Environmental company scrape (Google Maps via Places API)

Discovers Australian companies in environment / sustainability work (consultants, solar, waste, recycling, energy efficiency) and writes JSON. Uses **Places API (New)** — the same Maps-backed API as restaurant scrape. Does not scrape maps.google.com HTML.

## Setup

```bash
cd automation/environment_companies
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

Uses `PLACES_API` or `GOOGLE_PLACES_API_KEY` from (first found, existing env wins):

- this folder `.env`
- `automation/outreach/.env`
- `backend/.env`
- MonoRepo `.env`

Copy [`.env.example`](.env.example) if you need a local key.

## Run

```bash
# Plan only
python scrape_environment_companies.py --city Sydney --dry-run

python scrape_environment_companies.py --city Sydney --total 50
python scrape_environment_companies.py --cities Sydney Melbourne --total 30
```

Supported cities: Sydney, Melbourne, Perth, Adelaide, Brisbane.

Output: `output/environment_companies_<city>_<utc>.json`

## JSON

Each file has `meta` plus `companies[]` with `place_id`, `name`, address, phone, website, Maps URI, rating, types, hours, `matched_query`. Dedupe is by `place_id`. Permanently/temporarily closed listings are skipped.

## Employees (website Team/About pages)

Up to 10 named people from the company site — no Apollo/LinkedIn. Many sites list fewer than 10.

```bash
python scrape_company_employees.py --input output/environment_companies_sydney_20260819T191247Z.json
python scrape_company_employees.py --latest --max-employees 10
```

Writes `output/environment_companies_<city>_<utc>_employees.json` with `employees[]` (`name`, `title`, `email`, `source_url`) and `employee_scrape` stats. Original Places dump is not modified.
