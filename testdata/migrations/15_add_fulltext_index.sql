ALTER TABLE orders
  ADD FULLTEXT INDEX ft_orders_note (note);
