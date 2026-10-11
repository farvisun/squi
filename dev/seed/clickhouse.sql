-- ClickHouse playground: squi database (users 500, orders 20000, events 100000).
CREATE DATABASE IF NOT EXISTS squi;

CREATE TABLE squi.users
(
    id         UInt32 COMMENT 'user id',
    email      String,
    name       String,
    country    LowCardinality(String),
    created_at DateTime DEFAULT now(),
    tags       Array(String)
)
ENGINE = MergeTree
ORDER BY id
COMMENT 'Registered users';

CREATE TABLE squi.orders
(
    id         UInt64,
    user_id    UInt32,
    status     Enum8('pending' = 1, 'paid' = 2, 'shipped' = 3, 'cancelled' = 4),
    total      Decimal(12, 2),
    note       Nullable(String),
    created_at DateTime64(3),
    INDEX idx_status status TYPE set(4) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (user_id, id);

CREATE TABLE squi.events
(
    ts         DateTime,
    user_id    UInt32,
    kind       LowCardinality(String),
    props      Map(String, String),
    duration   Float64
)
ENGINE = MergeTree
PARTITION BY toDate(ts)
ORDER BY (kind, ts)
TTL ts + INTERVAL 5 YEAR;

CREATE TABLE squi.daily_revenue
(
    day     Date,
    orders  AggregateFunction(count),
    revenue AggregateFunction(sum, Decimal(12, 2))
)
ENGINE = AggregatingMergeTree
ORDER BY day;

CREATE MATERIALIZED VIEW squi.daily_revenue_mv TO squi.daily_revenue AS
SELECT toDate(created_at) AS day, countState() AS orders, sumState(total) AS revenue
FROM squi.orders
GROUP BY day;

CREATE VIEW squi.paid_orders AS
SELECT o.id, u.email, o.total, o.created_at
FROM squi.orders AS o
INNER JOIN squi.users AS u ON u.id = o.user_id
WHERE o.status = 'paid';

INSERT INTO squi.users (id, email, name, country, created_at, tags)
SELECT number + 1,
       concat('user', toString(number + 1), '@example.com'),
       concat('User ', toString(number + 1)),
       ['DE', 'GB', 'US', 'IR', 'FR'][number % 5 + 1],
       toDateTime('2026-01-01 00:00:00') + number * 3600,
       if(number % 3 = 0, ['beta', 'vip'], ['beta'])
FROM numbers(500);

INSERT INTO squi.orders
SELECT number + 1,
       number % 500 + 1,
       ['pending', 'paid', 'shipped', 'cancelled'][number % 4 + 1],
       toDecimal64((number + 1) * 1.31, 2),
       if(number % 10 = 0, 'gift', NULL),
       toDateTime64('2026-10-01 00:00:00', 3) - number * 60
FROM numbers(20000);

INSERT INTO squi.events
SELECT toDateTime('2026-10-01 00:00:00') - number * 30,
       number % 500 + 1,
       ['view', 'click', 'purchase', 'signup'][number % 4 + 1],
       map('page', concat('/p/', toString(number % 50)), 'ref', if(number % 2 = 0, 'ads', 'direct')),
       (number % 1000) / 10.0
FROM numbers(100000);
