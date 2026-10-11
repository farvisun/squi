CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped', 'cancelled');

CREATE TABLE users (
  id         bigserial PRIMARY KEY,
  email      text NOT NULL UNIQUE,
  name       text NOT NULL,
  country    char(2),
  meta       jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE users IS 'Registered customers';
COMMENT ON COLUMN users.email IS 'Login email, unique';

CREATE TABLE products (
  id    serial PRIMARY KEY,
  sku   varchar(32) NOT NULL UNIQUE,
  title text NOT NULL,
  price numeric(10,2) NOT NULL CHECK (price >= 0),
  tags  text[] NOT NULL DEFAULT '{}'
);

CREATE TABLE orders (
  id         bigserial PRIMARY KEY,
  user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status     order_status NOT NULL DEFAULT 'pending',
  total      numeric(12,2) NOT NULL DEFAULT 0,
  note       text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_user_idx ON orders(user_id);
CREATE INDEX orders_status_created_idx ON orders(status, created_at DESC);

CREATE TABLE order_items (
  order_id   bigint NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  product_id int NOT NULL REFERENCES products(id),
  qty        int NOT NULL CHECK (qty > 0),
  unit_price numeric(10,2) NOT NULL,
  PRIMARY KEY (order_id, product_id)
);

CREATE TABLE audit_log (ts timestamptz DEFAULT now(), message text);

CREATE FUNCTION touch_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
CREATE TRIGGER orders_touch BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE VIEW paid_orders AS SELECT o.*, u.email FROM orders o JOIN users u ON u.id = o.user_id WHERE o.status = 'paid';
CREATE MATERIALIZED VIEW revenue_by_country AS
  SELECT u.country, sum(o.total) AS revenue FROM orders o JOIN users u ON u.id = o.user_id GROUP BY 1;
CREATE SEQUENCE invoice_seq START 1000;

INSERT INTO users (email, name, country, meta)
SELECT 'user' || g || '@example.com', 'User ' || g, (ARRAY['GB','US','DE','FR','IR'])[1 + g % 5],
       jsonb_build_object('plan', CASE WHEN g % 3 = 0 THEN 'pro' ELSE 'free' END, 'score', g * 7 % 100)
FROM generate_series(1, 500) g;
INSERT INTO products (sku, title, price, tags)
SELECT 'SKU-' || lpad(g::text, 4, '0'), 'Product ' || g, (g * 3.7)::numeric(10,2), ARRAY['tag' || g % 4]
FROM generate_series(1, 50) g;
INSERT INTO orders (user_id, status, total, note, created_at)
SELECT 1 + g % 500, (ARRAY['pending','paid','shipped','cancelled']::order_status[])[1 + g % 4],
       (g * 1.31)::numeric(12,2), CASE WHEN g % 10 = 0 THEN 'gift wrap' END, now() - (g || ' minutes')::interval
FROM generate_series(1, 20000) g;
INSERT INTO order_items SELECT o.id, 1 + o.id % 50, 1 + o.id % 3, 9.99 FROM orders o;
REFRESH MATERIALIZED VIEW revenue_by_country;

CREATE SCHEMA analytics;
CREATE TABLE analytics.events (id bigserial PRIMARY KEY, kind text NOT NULL, payload jsonb, at timestamptz DEFAULT now());
INSERT INTO analytics.events (kind, payload) SELECT 'click', jsonb_build_object('n', g) FROM generate_series(1, 100) g;

CREATE DATABASE warehouse;
