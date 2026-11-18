ALTER TABLE orders
  ADD INDEX idx_orders_amount (amount),
  ALGORITHM = INSTANT;
