ALTER TABLE orders
  ADD UNIQUE INDEX uniq_orders_external_ref (external_ref);
