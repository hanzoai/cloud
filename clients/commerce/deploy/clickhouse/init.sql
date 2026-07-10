-- Hanzo Commerce - ClickHouse Analytics Schema
-- This file is automatically executed on first container startup

-- Create commerce database
CREATE DATABASE IF NOT EXISTS commerce;

-- ==============================================================================
-- ANALYTICS EVENTS
-- ==============================================================================

-- Main events table using MergeTree for fast analytics
CREATE TABLE IF NOT EXISTS commerce.events (
    -- Event identification
    event_id UUID DEFAULT generateUUIDv4(),
    event_type LowCardinality(String),
    event_name String,

    -- Temporal
    timestamp DateTime64(3) DEFAULT now64(3),
    date Date DEFAULT toDate(timestamp),

    -- Entity references
    org_id String,
    user_id String,
    session_id String,

    -- Commerce entities
    order_id String DEFAULT '',
    product_id String DEFAULT '',
    variant_id String DEFAULT '',
    cart_id String DEFAULT '',

    -- Event data (JSON)
    properties String DEFAULT '{}',

    -- Context
    ip String DEFAULT '',
    user_agent String DEFAULT '',
    referrer String DEFAULT '',
    url String DEFAULT '',

    -- Geo (enriched)
    country LowCardinality(String) DEFAULT '',
    region String DEFAULT '',
    city String DEFAULT '',

    -- Device (parsed from user_agent)
    device_type LowCardinality(String) DEFAULT '',
    browser LowCardinality(String) DEFAULT '',
    os LowCardinality(String) DEFAULT '',

    INDEX idx_event_type event_type TYPE set(100) GRANULARITY 4,
    INDEX idx_org_id org_id TYPE bloom_filter() GRANULARITY 4,
    INDEX idx_user_id user_id TYPE bloom_filter() GRANULARITY 4
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, event_type, timestamp)
TTL date + INTERVAL 2 YEAR
SETTINGS index_granularity = 8192;

-- ==============================================================================
-- ORDER ANALYTICS
-- ==============================================================================

CREATE TABLE IF NOT EXISTS commerce.orders (
    -- Order identification
    order_id String,
    order_number UInt64,

    -- Temporal
    created_at DateTime64(3),
    updated_at DateTime64(3),
    date Date DEFAULT toDate(created_at),

    -- Organization
    org_id String,
    store_id String DEFAULT '',

    -- Customer
    user_id String,
    email String DEFAULT '',

    -- Financials (in cents)
    subtotal Int64 DEFAULT 0,
    shipping Int64 DEFAULT 0,
    tax Int64 DEFAULT 0,
    discount Int64 DEFAULT 0,
    total Int64 DEFAULT 0,
    currency LowCardinality(String) DEFAULT 'USD',

    -- Items
    item_count UInt32 DEFAULT 0,

    -- Status
    status LowCardinality(String) DEFAULT 'pending',
    payment_status LowCardinality(String) DEFAULT 'pending',
    fulfillment_status LowCardinality(String) DEFAULT 'unfulfilled',

    -- Payment
    payment_method LowCardinality(String) DEFAULT '',
    payment_processor LowCardinality(String) DEFAULT '',

    -- Shipping
    shipping_method LowCardinality(String) DEFAULT '',
    shipping_country LowCardinality(String) DEFAULT '',

    -- Attribution
    source String DEFAULT '',
    campaign String DEFAULT '',
    coupon_code String DEFAULT '',
    referrer_id String DEFAULT '',

    -- Flags
    is_test UInt8 DEFAULT 0,
    is_subscription UInt8 DEFAULT 0,

    INDEX idx_status status TYPE set(20) GRANULARITY 4,
    INDEX idx_user_id user_id TYPE bloom_filter() GRANULARITY 4,
    INDEX idx_email email TYPE bloom_filter() GRANULARITY 4
)
ENGINE = ReplacingMergeTree(updated_at)
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, order_id)
TTL date + INTERVAL 7 YEAR
SETTINGS index_granularity = 8192;

-- ==============================================================================
-- PRODUCT ANALYTICS
-- ==============================================================================

CREATE TABLE IF NOT EXISTS commerce.product_views (
    -- View identification
    view_id UUID DEFAULT generateUUIDv4(),

    -- Temporal
    timestamp DateTime64(3) DEFAULT now64(3),
    date Date DEFAULT toDate(timestamp),

    -- Organization
    org_id String,

    -- Product
    product_id String,
    variant_id String DEFAULT '',
    sku String DEFAULT '',

    -- Viewer
    user_id String DEFAULT '',
    session_id String,

    -- Context
    source String DEFAULT '',
    referrer String DEFAULT '',

    INDEX idx_product_id product_id TYPE bloom_filter() GRANULARITY 4
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, product_id, timestamp)
TTL date + INTERVAL 1 YEAR
SETTINGS index_granularity = 8192;

-- ==============================================================================
-- TRANSACTION ANALYTICS
-- ==============================================================================

CREATE TABLE IF NOT EXISTS commerce.transactions (
    -- Transaction identification
    transaction_id String,

    -- Temporal
    created_at DateTime64(3),
    date Date DEFAULT toDate(created_at),

    -- Organization
    org_id String,

    -- References
    order_id String DEFAULT '',
    user_id String,

    -- Financials (in cents)
    amount Int64,
    fee Int64 DEFAULT 0,
    net Int64 DEFAULT 0,
    currency LowCardinality(String) DEFAULT 'USD',

    -- Type and status
    type LowCardinality(String),  -- 'charge', 'refund', 'transfer', 'hold'
    status LowCardinality(String) DEFAULT 'pending',

    -- Processor
    processor LowCardinality(String) DEFAULT '',
    processor_id String DEFAULT '',

    -- Metadata
    description String DEFAULT '',
    metadata String DEFAULT '{}',

    INDEX idx_type type TYPE set(10) GRANULARITY 4,
    INDEX idx_status status TYPE set(10) GRANULARITY 4,
    INDEX idx_order_id order_id TYPE bloom_filter() GRANULARITY 4
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, transaction_id)
TTL date + INTERVAL 7 YEAR
SETTINGS index_granularity = 8192;

-- ==============================================================================
-- AGGREGATED VIEWS (Materialized)
-- ==============================================================================

-- Daily sales summary
CREATE MATERIALIZED VIEW IF NOT EXISTS commerce.daily_sales_mv
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, currency)
AS SELECT
    org_id,
    date,
    currency,
    count() AS order_count,
    sum(total) AS revenue,
    sum(discount) AS discounts,
    sum(shipping) AS shipping_revenue,
    sum(tax) AS tax_collected,
    sum(item_count) AS items_sold,
    countIf(is_subscription = 1) AS subscription_orders
FROM commerce.orders
WHERE is_test = 0
GROUP BY org_id, date, currency;

-- Hourly event counts
CREATE MATERIALIZED VIEW IF NOT EXISTS commerce.hourly_events_mv
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, hour, event_type)
AS SELECT
    org_id,
    date,
    toHour(timestamp) AS hour,
    event_type,
    count() AS event_count,
    uniqExact(user_id) AS unique_users,
    uniqExact(session_id) AS unique_sessions
FROM commerce.events
GROUP BY org_id, date, hour, event_type;

-- ==============================================================================
-- SYSTEM TABLES
-- ==============================================================================

-- Audit log for data changes
CREATE TABLE IF NOT EXISTS commerce.audit_log (
    id UUID DEFAULT generateUUIDv4(),
    timestamp DateTime64(3) DEFAULT now64(3),
    date Date DEFAULT toDate(timestamp),

    org_id String,
    user_id String,

    action LowCardinality(String),
    entity_type LowCardinality(String),
    entity_id String,

    old_value String DEFAULT '',
    new_value String DEFAULT '',

    ip String DEFAULT '',
    user_agent String DEFAULT ''
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (org_id, date, timestamp)
TTL date + INTERVAL 2 YEAR
SETTINGS index_granularity = 8192;

-- System health metrics
CREATE TABLE IF NOT EXISTS commerce.system_metrics (
    timestamp DateTime64(3) DEFAULT now64(3),
    date Date DEFAULT toDate(timestamp),

    metric_name LowCardinality(String),
    metric_value Float64,

    tags String DEFAULT '{}'
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, metric_name, timestamp)
TTL date + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
