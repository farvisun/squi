-- SQLite playground seed: `make dev-sqlite` writes it to dev/squi.db.
PRAGMA foreign_keys = ON;

CREATE TABLE users (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  email      TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  country    TEXT CHECK (length(country) = 2),
  meta       TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
  id    INTEGER PRIMARY KEY,
  sku   TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  price NUMERIC NOT NULL CONSTRAINT price_positive CHECK (price >= 0),
  image BLOB
);

CREATE TABLE orders (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,
  status     TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled')),
  total      REAL NOT NULL DEFAULT 0,
  note       TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE INDEX orders_user_idx ON orders(user_id);
CREATE INDEX orders_status_created_idx ON orders(status, created_at DESC);

CREATE TABLE order_items (
  order_id   INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  product_id INTEGER NOT NULL REFERENCES products(id),
  qty        INTEGER NOT NULL CHECK (qty > 0),
  unit_price REAL NOT NULL,
  PRIMARY KEY (order_id, product_id)
) WITHOUT ROWID;

CREATE TABLE audit_log (ts TEXT DEFAULT CURRENT_TIMESTAMP, message TEXT);

CREATE TRIGGER orders_touch AFTER UPDATE OF status, total, note ON orders
BEGIN
  UPDATE orders SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = NEW.id;
END;

CREATE VIEW paid_orders AS SELECT o.*, u.email FROM orders o JOIN users u ON u.id = o.user_id WHERE o.status = 'paid';

WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 500)
INSERT INTO users (email, name, country, meta)
SELECT 'user' || n || '@example.com', 'User ' || n, substr('GBUSDEFRIR', 1 + (n % 5) * 2, 2),
       json_object('plan', CASE WHEN n % 3 = 0 THEN 'pro' ELSE 'free' END, 'score', n * 7 % 100)
FROM g;
WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 50)
INSERT INTO products (sku, title, price, image)
SELECT 'SKU-' || printf('%04d', n), 'Product ' || n, round(n * 3.7, 2), CASE WHEN n = 1 THEN x'CAFEBABE' END FROM g;
WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 20000)
INSERT INTO orders (user_id, status, total, note, created_at)
SELECT 1 + n % 500, CASE n % 4 WHEN 0 THEN 'pending' WHEN 1 THEN 'paid' WHEN 2 THEN 'shipped' ELSE 'cancelled' END,
       round(n * 1.31, 2), CASE WHEN n % 10 = 0 THEN 'gift wrap' END,
       strftime('%Y-%m-%d %H:%M:%f', 'now', '-' || n || ' minutes')
FROM g;
INSERT INTO order_items SELECT id, 1 + id % 50, 1 + id % 3, 9.99 FROM orders;
ANALYZE;
