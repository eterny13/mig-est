ALTER TABLE orders
  ADD CONSTRAINT chk_orders_amount CHECK (amount >= 0);
