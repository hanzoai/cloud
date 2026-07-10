# Hanzo Billing Service - ZAP Schema
# Single source of truth for all balance/usage/transaction operations.
#
# Server:  Commerce (Go/Gin) at commerce.hanzo.ai
# Clients: Cloud-API (Go), LLM Gateway (Python), Console (TypeScript), hanzo/node (Rust)
#
# All monetary amounts are in cents (Int64).
# Currency codes are lowercase ISO 4217 (e.g. "usd", "eur", "jpy").
#
# Code generation:
#   zapc generate billing.zap --lang go     --out ./gen/go/
#   zapc generate billing.zap --lang python  --out ./gen/python/
#   zapc generate billing.zap --lang ts      --out ./gen/ts/
#   zapc generate billing.zap --lang rust    --out ./gen/rust/

# =============================================================================
# Transaction Types
# =============================================================================

enum TransactionType
  hold
  holdRemoved
  transfer
  deposit
  withdraw

# =============================================================================
# Balance
# =============================================================================

struct Balance
  user Text
  currency Text
  balance Int64       # total balance in cents
  holds Int64         # held amount in cents
  available Int64     # balance - holds, clamped >= 0

struct BalanceEntry
  currency Text
  balance Int64
  holds Int64
  available Int64

struct BalanceAllResponse
  user Text
  balances List(BalanceEntry)

# =============================================================================
# Usage
# =============================================================================

struct UsageRecord
  user Text
  currency Text
  amount Int64        # cost in cents
  model Text
  provider Text
  promptTokens Int32
  completionTokens Int32
  totalTokens Int32
  requestId Text
  premium Bool
  stream Bool
  status Text
  clientIp Text

struct UsageResult
  transactionId Text
  user Text
  amount Int64
  currency Text
  type TransactionType

struct UsageQuery
  user Text
  currency Text

struct UsageItem
  transactionId Text
  amount Int64
  currency Text
  notes Text
  metadata Data
  createdAt Text

struct UsageList
  user Text
  count Int32
  usage List(UsageItem)

# =============================================================================
# Deposit
# =============================================================================

struct DepositRequest
  user Text
  currency Text
  amount Int64
  notes Text
  tags Text
  metadata Data

struct TransactionResult
  transactionId Text
  user Text
  amount Int64
  currency Text
  type TransactionType

# =============================================================================
# SBOM-driven OSS-developer payout
# =============================================================================
#
# Every arcd deploy emits an SBOM of the built image; commerce attributes up to
# 25% of each org's cloud spend across the OSS packages present in those SBOMs
# (pro-rata by weight) and accrues each package's share to a per-maintainer
# ledger. The cap (0.25) and weighting are policy (config/oss-payout.json),
# enforced in code (ossattr.MaxPoolFraction).
#
# ZAP opcode (transport): OpSBOMIngest = 0x20 — same node as vector ops.

struct SBOMComponent
  purl Text          # canonical Package URL — the attribution join key
  name Text
  ecosystem Text     # golang | npm | pypi | apk | deb | ...
  version Text
  scope Text         # direct | transitive

struct SBOMIngest
  imageRef Text      # ghcr.io/<org>/<svc>:<tag>
  imageDigest Text   # sha256:... — immutable idempotency key
  service Text       # logical Hanzo service
  format Text        # cyclonedx | spdx
  tool Text          # syft
  components List(SBOMComponent)

struct SBOMIngestResult
  id Text
  imageDigest Text
  service Text
  componentCount Int32

struct OSSAccrualLine
  purl Text
  name Text
  scope Text
  spendOrg Text
  sourceTxnId Text
  amount Int64       # cents accrued to this package
  currency Text
  status Text        # pending | held | queued | settled
  fundingTarget Text # e.g. github_sponsors:gin-gonic
  shareRatio Float64

struct OSSAccrualList
  count Int32
  accruals List(OSSAccrualLine)

struct OSSMaintainerSummary
  purl Text
  name Text
  ecosystem Text
  fundingTarget Text
  fundingKind Text
  currency Text
  accruedCents Int64
  lines Int32
  status Text

struct OSSPayoutSummary
  totalAccruedCents Int64
  heldCents Int64
  packageCount Int32
  maintainers List(OSSMaintainerSummary)

# =============================================================================
# Service Interface
# =============================================================================

interface BillingService
  # GET /v1/billing/balance?user=&currency=
  getBalance (user Text, currency Text) -> (balance Balance)

  # GET /v1/billing/balance/all?user=
  getBalanceAll (user Text) -> (response BalanceAllResponse)

  # GET /v1/billing/usage?user=&currency=
  getUsage (query UsageQuery) -> (list UsageList)

  # POST /v1/billing/usage
  recordUsage (record UsageRecord) -> (result UsageResult)

  # POST /v1/billing/deposit (future)
  deposit (request DepositRequest) -> (result TransactionResult)

  # POST /v1/billing/sbom  (ZAP: OpSBOMIngest 0x20)
  # Ingest the SBOM of a deployed image. Idempotent on imageDigest.
  ingestSBOM (sbom SBOMIngest) -> (result SBOMIngestResult)

  # GET /v1/billing/oss-accruals?purl=&org=
  listOSSAccruals (purl Text, org Text) -> (list OSSAccrualList)

  # GET /v1/billing/oss-payout/summary?org=
  # Per-package payout rollup — the disbursement-ready view.
  getOSSPayoutSummary (org Text) -> (summary OSSPayoutSummary)
