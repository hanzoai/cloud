# Hanzo Company — dogfood runbook (Hanzo / Lux / Zoo import)

Hanzo, Lux, and Zoo are already incorporated. This runbook walks each of them
through the **import path** of Hanzo Company (`/v1/company`): skip formation, pull
their corporate documents from Google Drive into the data room, pull their cap
table from a Google Sheet into captable, and land the org at the `company` stage.

Everything below uses the real `/v1/company` + `/v1/integrations` surfaces. It uses
placeholders (`<...>`) for the per-org Google Drive folder / Sheet ids — fill those
in from each org's real Drive (there is nothing fabricated here; the ids are the
only inputs you supply).

## Model recap

The state machine has two paths. Already-incorporated orgs take the SKIP path:

```
structure --skip--> import --(docs + cap table)--> company
```

The import path never touches KYC, the $999 fee, document generation, e-sign, or the
on-chain equity genesis — those are the greenfield-formation path only.

## Per-deployment hosts (white-label)

| Org   | API host          | Brand |
|-------|-------------------|-------|
| Hanzo | `api.hanzo.ai`    | hanzo |
| Lux   | `api.lux.network` | lux   |
| Zoo   | `api.zoo.ngo`     | zoo   |

All requests carry the org's validated bearer (the gateway mints `X-Org-Id` /
`X-User-Id` from it). Below, `$H` is the host and `$T` the bearer for the org you
are running.

## Step 0 — connect Google (one-time, per org)

Drive + Sheets import reads through the org's Google OAuth token, custodied in KMS by
the `google` provider in `clients/integrations`. Connect it once:

```bash
# The google card shows available=true once GOOGLE_CLIENT_ID/SECRET are set on the
# deployment (KMS-synced). List providers to confirm:
curl -s "$H/v1/integrations" -H "Authorization: Bearer $T" | jq '.[] | select(.id=="google")'

# Begin the OAuth consent (opens Google, returns here to the callback, which seals
# access_token + refresh_token into this org's KMS namespace):
open "$H/v1/integrations/google/connect"     # or GET it and follow the 302
```

After consent, `GET /v1/integrations` shows `google` with `connected: true` and the
account label (the Google account email). The same token now powers both the
automations `google` connector and this import.

**Get the folder / sheet ids** from the org's Drive:
- Folder id: open the Drive folder of corporate docs; the id is the last URL
  segment (`drive.google.com/drive/folders/<FOLDER_ID>`).
- Sheet id: open the cap-table spreadsheet; the id is the URL segment after `/d/`
  (`docs.google.com/spreadsheets/d/<SHEET_ID>/edit`).

The cap-table sheet's first row must be a header with at least `Name` and `Email`
columns (optional: `Type` = INDIVIDUAL|INSTITUTION, `Relationship` =
FOUNDER|INVESTOR|EMPLOYEE|…, `Institution`).

## Step 1 — begin + skip

```bash
# Declare the org already-incorporated and begin the formation record.
curl -s -X POST "$H/v1/company" -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d '{"alreadyIncorporated": true}' | jq .

# Take the skip: structure -> import.
curl -s -X POST "$H/v1/company/skip" -H "Authorization: Bearer $T" | jq '.formation.stage'
# => "import"
```

## Step 2 — import corporate documents (Drive -> data room)

```bash
curl -s -X POST "$H/v1/company/import/documents" -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"folderId\": \"<DRIVE_FOLDER_ID>\"}" | jq '{ingested, stage: .formation.stage}'
```

Every file in the folder is downloaded (Google-native Docs/Sheets/Slides are exported
to PDF/CSV) and ingested into the org's data room; the returned `ingested` count is
the number of documents now in `/v1/dataroom`.

## Step 3 — import the cap table (Sheet -> captable)

```bash
curl -s -X POST "$H/v1/company/import/captable" -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"spreadsheetId\": \"<CAP_TABLE_SHEET_ID>\", \"range\": \"A1:D1000\"}" \
  | jq '{stakeholdersImported, rows}'
```

Each data row becomes a stakeholder in `/v1/captable`. `range` is optional
(defaults to `A1:Z1000`).

## Step 4 — finish (import -> company)

```bash
curl -s -X POST "$H/v1/company/advance" -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d '{"to": "company"}' \
  | jq '.formation.stage'
# => "company"
```

Reaching `company` records the incorporation on the canonical captable company row
(`incorporation_type` + jurisdiction). The org is now a company; the fundraising loop
(`/v1/company/fundraise/*`) is available.

## The three orgs

Run the same four steps per org with its host, bearer, and Drive ids:

### Hanzo (`api.hanzo.ai`)
```
H=https://api.hanzo.ai
DRIVE_FOLDER_ID=<hanzo corporate-docs folder>
CAP_TABLE_SHEET_ID=<hanzo cap-table sheet>
```
Hanzo AI is a Delaware C-Corp (Techstars '17). Its data room holds the incorporation
docs, board consents, and prior SAFE/note instruments; the cap-table sheet holds the
founder + investor stakeholders.

### Lux (`api.lux.network`)
```
H=https://api.lux.network
DRIVE_FOLDER_ID=<lux corporate-docs folder>
CAP_TABLE_SHEET_ID=<lux cap-table sheet>
```
Lux imports its corporate records + cap table the same way. (Lux's on-chain
securities live on the Lux network; this import brings the off-chain corporate
document set + stakeholder list into the data room / captable.)

### Zoo (`api.zoo.ngo`)
```
H=https://api.zoo.ngo
DRIVE_FOLDER_ID=<zoo corporate-docs folder>
CAP_TABLE_SHEET_ID=<zoo cap-table sheet>
```
Zoo Labs Foundation imports its foundation documents + membership/cap table.

## Fundraising after import (optional)

Once at `company`, a round can be recorded and the deck shared:

```bash
# Record a priced or SAFE round in the cap table.
curl -s -X POST "$H/v1/company/fundraise/round" -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Seed","roundType":"PRICED","targetAmount":2000000,"preMoneyValuation":8000000}'

# Share the pitch deck into the data room (raw bytes as the body).
curl -s -X POST "$H/v1/company/fundraise/deck?name=deck.pdf" \
  -H "Authorization: Bearer $T" -H 'Content-Type: application/pdf' \
  --data-binary @deck.pdf
```

## Honest gaps (what is real vs pending)

- **Google token refresh.** The OAuth flow seals `access_token` + `refresh_token` in
  KMS. Access tokens expire in ~1h; a refresh seam (mint a fresh access token from
  the refresh token inside `clients/integrations`) is not yet wired, so a long-idle
  connection must re-consent. Import runs immediately after connect, so this does not
  affect the runbook.
- **E-sign completion.** The fundraising SAFE/note signature request
  (`/v1/company/fundraise/safe`) returns a reference from the esign seam; the
  `clients/sign` bundle's create→recipients→fields→send sequence has no synchronous
  in-process Go seam yet, so completion is driven by an explicit signal, not a live
  provider webhook. See the PR's gap list.
- **State filing.** Not part of the import path (already-incorporated orgs are already
  filed). For greenfield formation, filing is an honest stub — see
  `clients/company/filing.go`.
- **Drive import is shallow.** Only files directly in the named folder are imported;
  sub-folders are skipped. Point the import at the folder that holds the documents.
