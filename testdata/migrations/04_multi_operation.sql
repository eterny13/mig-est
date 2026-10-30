-- One statement with both a cheap and an expensive operation.
-- The whole statement must be treated as COPY.
ALTER TABLE orders
  ADD COLUMN note TEXT,
  ADD PRIMARY KEY (id);
