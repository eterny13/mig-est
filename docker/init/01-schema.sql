-- Local-only schema for mig-est integration smoke tests.
-- These credentials are intentionally disposable and must never be used
-- outside the Docker compose environment.

CREATE TABLE customers (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  email VARCHAR(255) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_customers_email (email)
) ENGINE=InnoDB;

CREATE TABLE orders (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  customer_id BIGINT UNSIGNED NOT NULL,
  status VARCHAR(20) NOT NULL DEFAULT 'new',
  amount DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  note TEXT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_orders_customer (customer_id),
  KEY idx_orders_status (status),
  CONSTRAINT fk_orders_customer FOREIGN KEY (customer_id) REFERENCES customers (id)
) ENGINE=InnoDB;

CREATE TABLE audit_log (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  order_id BIGINT UNSIGNED NOT NULL,
  action VARCHAR(20) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_audit_order (order_id)
) ENGINE=InnoDB;

INSERT INTO customers (email) VALUES
  ('alice@example.test'),
  ('bob@example.test');

INSERT INTO orders (customer_id, status, amount) VALUES
  (1, 'paid', 19.95),
  (2, 'new', 42.00),
  (1, 'shipped', 7.50);

-- The example read-only account from the README. The Docker root user runs
-- this script, so it is safe for this local-only database.
CREATE USER 'migread'@'%' IDENTIFIED BY 'migread';
GRANT SELECT ON appdb.* TO 'migread'@'%';
-- information_schema is exposed through object privileges; a direct GRANT on
-- information_schema.* is rejected by MySQL 8.0.
GRANT SELECT ON performance_schema.* TO 'migread'@'%';
-- PROCESS lets the MDL checks see sessions other than mig-est's own. It does
-- not grant write access.
GRANT PROCESS ON *.* TO 'migread'@'%';
GRANT REPLICATION CLIENT ON *.* TO 'migread'@'%';
FLUSH PRIVILEGES;
