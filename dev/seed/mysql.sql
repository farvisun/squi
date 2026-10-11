CREATE TABLE users (
  id         BIGINT AUTO_INCREMENT PRIMARY KEY,
  email      VARCHAR(255) NOT NULL UNIQUE COMMENT 'Login email, unique',
  name       VARCHAR(255) NOT NULL,
  country    CHAR(2),
  meta       JSON,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB COMMENT='Registered customers';

CREATE TABLE products (
  id    INT AUTO_INCREMENT PRIMARY KEY,
  sku   VARCHAR(32) NOT NULL UNIQUE,
  title VARCHAR(255) NOT NULL,
  price DECIMAL(10,2) NOT NULL,
  CONSTRAINT price_positive CHECK (price >= 0)
);

CREATE TABLE orders (
  id         BIGINT AUTO_INCREMENT PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  status     ENUM('pending','paid','shipped','cancelled') NOT NULL DEFAULT 'pending',
  total      DECIMAL(12,2) NOT NULL DEFAULT 0,
  note       TEXT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY orders_status_created_idx (status, created_at),
  CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE order_items (
  order_id   BIGINT NOT NULL,
  product_id INT NOT NULL,
  qty        INT NOT NULL,
  unit_price DECIMAL(10,2) NOT NULL,
  PRIMARY KEY (order_id, product_id),
  FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
  FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE TABLE audit_log (ts TIMESTAMP DEFAULT CURRENT_TIMESTAMP, message TEXT);

CREATE TRIGGER orders_audit AFTER UPDATE ON orders FOR EACH ROW
  INSERT INTO audit_log (message) VALUES (CONCAT('order ', NEW.id, ' updated'));

CREATE VIEW paid_orders AS SELECT o.*, u.email FROM orders o JOIN users u ON u.id = o.user_id WHERE o.status = 'paid';

DELIMITER //
CREATE PROCEDURE user_orders(IN uid BIGINT)
BEGIN
  SELECT * FROM orders WHERE user_id = uid;
END //
CREATE FUNCTION order_count(uid BIGINT) RETURNS INT DETERMINISTIC READS SQL DATA
BEGIN
  DECLARE n INT;
  SELECT COUNT(*) INTO n FROM orders WHERE user_id = uid;
  RETURN n;
END //
DELIMITER ;

INSERT INTO users (email, name, country, meta)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 500)
SELECT CONCAT('user', n, '@example.com'), CONCAT('User ', n), ELT(1 + n % 5, 'GB','US','DE','FR','IR'),
       JSON_OBJECT('plan', IF(n % 3 = 0, 'pro', 'free'), 'score', n * 7 % 100) FROM seq;
INSERT INTO products (sku, title, price)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 50)
SELECT CONCAT('SKU-', LPAD(n, 4, '0')), CONCAT('Product ', n), n * 3.7 FROM seq;
/*!80000 SET SESSION cte_max_recursion_depth = 100000 */; /*M!100000 SET SESSION max_recursive_iterations = 100000 */;
INSERT INTO orders (user_id, status, total, note, created_at)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 20000)
SELECT 1 + n % 500, ELT(1 + n % 4, 'pending','paid','shipped','cancelled'), n * 1.31,
       IF(n % 10 = 0, 'gift wrap', NULL), NOW() - INTERVAL n MINUTE FROM seq;
INSERT INTO order_items SELECT id, 1 + id % 50, 1 + id % 3, 9.99 FROM orders;

CREATE DATABASE IF NOT EXISTS warehouse;
CREATE TABLE warehouse.stock (sku VARCHAR(32) PRIMARY KEY, qty INT NOT NULL DEFAULT 0);
GRANT ALL ON warehouse.* TO 'squi'@'%';
GRANT PROCESS ON *.* TO 'squi'@'%';
