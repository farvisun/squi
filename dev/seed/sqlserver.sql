-- SQL Server playground seed, run by the mssql-seed service with sqlcmd.
IF SUSER_ID('squi') IS NULL
  CREATE LOGIN squi WITH PASSWORD = 'squi', CHECK_POLICY = OFF, CHECK_EXPIRATION = OFF;
GO
IF DB_ID('squi') IS NOT NULL
BEGIN
  PRINT 'already seeded';
  SET NOEXEC ON;
END
GO
CREATE DATABASE squi;
GO
CREATE DATABASE warehouse;
GO
USE squi;
CREATE USER squi FOR LOGIN squi;
ALTER ROLE db_owner ADD MEMBER squi;
GO
CREATE TABLE users (
  id         bigint IDENTITY(1,1) PRIMARY KEY,
  email      nvarchar(200) NOT NULL UNIQUE,
  name       nvarchar(100) NOT NULL,
  country    char(2),
  meta       nvarchar(max) NOT NULL DEFAULT '{}',
  created_at datetimeoffset NOT NULL DEFAULT SYSDATETIMEOFFSET()
);
EXEC sp_addextendedproperty 'MS_Description', 'Registered customers', 'SCHEMA', 'dbo', 'TABLE', 'users';
EXEC sp_addextendedproperty 'MS_Description', 'Login email, unique', 'SCHEMA', 'dbo', 'TABLE', 'users', 'COLUMN', 'email';

CREATE TABLE products (
  id    int IDENTITY(1,1) PRIMARY KEY,
  sku   varchar(32) NOT NULL UNIQUE,
  title nvarchar(200) NOT NULL,
  price decimal(10,2) NOT NULL CHECK (price >= 0),
  guid  uniqueidentifier NOT NULL DEFAULT NEWID()
);

CREATE TABLE orders (
  id         bigint IDENTITY(1,1) PRIMARY KEY,
  user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status     varchar(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled')),
  total      decimal(12,2) NOT NULL DEFAULT 0,
  note       nvarchar(max),
  created_at datetime2(3) NOT NULL DEFAULT SYSUTCDATETIME(),
  updated_at datetime2(3) NOT NULL DEFAULT SYSUTCDATETIME()
);
CREATE INDEX orders_user_idx ON orders(user_id);
CREATE INDEX orders_status_created_idx ON orders(status, created_at DESC) INCLUDE (total);

CREATE TABLE order_items (
  order_id   bigint NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  product_id int NOT NULL REFERENCES products(id),
  qty        int NOT NULL CHECK (qty > 0),
  unit_price decimal(10,2) NOT NULL,
  PRIMARY KEY (order_id, product_id)
);

CREATE TABLE audit_log (ts datetime2 DEFAULT SYSUTCDATETIME(), message nvarchar(max), raw varbinary(16));
CREATE SEQUENCE invoice_seq AS bigint START WITH 1000;
GO
CREATE TRIGGER orders_touch ON orders AFTER UPDATE AS
BEGIN
  SET NOCOUNT ON;
  UPDATE o SET updated_at = SYSUTCDATETIME() FROM orders o JOIN inserted i ON i.id = o.id;
END;
GO
CREATE VIEW paid_orders AS
  SELECT o.*, u.email FROM orders o JOIN users u ON u.id = o.user_id WHERE o.status = 'paid';
GO
CREATE PROCEDURE orders_for_user @user_id bigint AS
  SELECT * FROM orders WHERE user_id = @user_id ORDER BY created_at DESC;
GO
INSERT INTO users (email, name, country, meta)
SELECT CONCAT('user', g.value, '@example.com'), CONCAT('User ', g.value),
       CHOOSE(1 + g.value % 5, 'GB', 'US', 'DE', 'FR', 'IR'),
       CONCAT('{"plan":"', IIF(g.value % 3 = 0, 'pro', 'free'), '","score":', g.value * 7 % 100, '}')
FROM GENERATE_SERIES(1, 500) g;
INSERT INTO products (sku, title, price)
SELECT CONCAT('SKU-', RIGHT(CONCAT('0000', g.value), 4)), CONCAT('Product ', g.value), CAST(g.value * 3.7 AS decimal(10,2))
FROM GENERATE_SERIES(1, 50) g;
INSERT INTO orders (user_id, status, total, note, created_at)
SELECT 1 + g.value % 500, CHOOSE(1 + g.value % 4, 'pending', 'paid', 'shipped', 'cancelled'),
       CAST(g.value * 1.31 AS decimal(12,2)), IIF(g.value % 10 = 0, 'gift wrap', NULL),
       DATEADD(minute, -g.value, SYSUTCDATETIME())
FROM GENERATE_SERIES(1, 20000) g;
INSERT INTO order_items SELECT o.id, 1 + o.id % 50, 1 + o.id % 3, 9.99 FROM orders o;
INSERT INTO audit_log (message, raw) VALUES (N'seeded ✓', 0xCAFEBABE);
GO
CREATE SCHEMA analytics;
GO
CREATE TABLE analytics.events (id bigint IDENTITY PRIMARY KEY, kind varchar(20) NOT NULL, payload nvarchar(max), at datetime2 DEFAULT SYSUTCDATETIME());
INSERT INTO analytics.events (kind, payload) SELECT 'click', CONCAT('{"n":', g.value, '}') FROM GENERATE_SERIES(1, 100) g;
GO
USE warehouse;
CREATE USER squi FOR LOGIN squi;
ALTER ROLE db_owner ADD MEMBER squi;
CREATE TABLE facts (id int PRIMARY KEY, amount decimal(12,2));
INSERT INTO facts VALUES (1, 10.50), (2, 20.25);
GO
