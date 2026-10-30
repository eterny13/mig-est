# Local MySQL for mig-est

The compose file starts MySQL 8.0 and 8.4 with the same schema from
`init/01-schema.sql`. The schema contains an `orders` table, a foreign-key
child table, and a read-only `migread` user.

```bash
cd docker
docker compose up -d
docker compose ps
```

Connection strings:

```text
# MySQL 8.0, full read/write local user
migest:migest@tcp(127.0.0.1:33080)/appdb

# MySQL 8.0, read-only user (recommended for mig-est)
migread:migread@tcp(127.0.0.1:33080)/appdb

# MySQL 8.4
migread:migread@tcp(127.0.0.1:33084)/appdb
```

Run the online integration smoke test:

```bash
MIGEST_TEST_DSN='migread:migread@tcp(127.0.0.1:33080)/appdb' \
  go test ./internal/catalog -run Integration -v
```

Stop and remove the local databases:

```bash
docker compose down -v
```

These credentials and ports are only for local development.
