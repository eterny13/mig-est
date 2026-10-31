# mig-est

`mig-est` is a read-only diagnostic CLI for MySQL schema migrations. Before an
`ALTER TABLE` reaches production, it predicts the online-DDL algorithm
(`INSTANT`, `INPLACE`, `COPY`, or `UNKNOWN`), checks table metadata and current
locks, estimates a time range and disk requirement, and suggests safer
alternatives.

> **Status: v0.1 prototype.** The binary, offline/online paths, reports, and
> calibration history work. Prebuilt release binaries and Homebrew packaging
> are not published yet. Initial single-run benchmark results are listed below;
> build from source and test against a staging database first.

> **mig-est never executes a migration.** It parses SQL as text and, in online
> mode, sends only `SELECT` and `SHOW` queries. There is no DDL/DML execution
> path.

The v0.1 specification [`docs/spec.md`](docs/spec.md).

## What it does

- Predicts the algorithm MySQL is expected to use for supported DDL.
- Applies MySQL 8.0/8.4 version gates (for example, positioned
  `ADD COLUMN` and `DROP COLUMN` before/after 8.0.29).
- Uses live table metadata: size, rows, engine, row format, columns, indexes,
  foreign keys, triggers, and partitions.
- Flags blockers such as long transactions, metadata-lock waits, idle
  transactions, replica lag or failure, missing primary keys, unsupported
  engines, and insufficient disk space.
- Reports time as a range plus a confidence level, never as a pretended exact
  number.
- Calibrates COPY/INPLACE estimates from the median of recent local
  `record` entries.
- Requires an explicit `--fail-on` gate for CI and emits text, Markdown, or
  JSON.
- Keeps SQL and credentials out of calibration history.

It is deliberately **not** Flyway/Alembic/goose, gh-ost, or pt-online-schema-change:
it does not run or rewrite a migration. It tells you what to review before you
run one.

## Supported targets

| Item | Support |
| --- | --- |
| MySQL | 8.0.x and 8.4.x |
| Storage engine | InnoDB (other engines are reported as unsupported) |
| Input | `ALTER TABLE`, `CREATE INDEX`, `DROP INDEX`, `CREATE TABLE`, `DROP TABLE`, `RENAME TABLE` |
| Out of scope | Stored programs, triggers, events, and `DELIMITER` blocks are reported as unsupported; DML and views are listed as out of scope |
| Database families | MariaDB, MySQL 5.7, and PostgreSQL are not supported |

## Build

Requirements: Go 1.26 or newer.

```bash
git clone <repository-url>
cd mig-est
make build
./bin/mig-est version
```

Or without Make:

```bash
go build -trimpath -o ./bin/mig-est ./cmd/mig-est
```

Run the tests:

```bash
go test ./...
go vet ./...
```

## Quick start: offline mode

Offline mode is the fastest way to try the tool. It requires no database
connection, but you must provide table statistics for useful estimates.

```bash
./bin/mig-est check \
  --offline \
  --file ./testdata/migrations/03_copy_charset.sql \
  --table-size orders=8200MB \
  --table-rows orders=12400000 \
  --disk-free 50GB \
  --format text
```

Repeat `--table-size` and `--table-rows` for each target table:

```bash
./bin/mig-est check --offline \
  --file ./migrations/*.sql \
  --table-size orders=8200MB \
  --table-size order_items=1200MB \
  --table-rows orders=12400000 \
  --table-rows order_items=35000000 \
  --disk-free 50GB \
  --fail-on high
```

If no DSN is supplied, mig-est automatically falls back to offline mode. An
explicit `--offline` always wins and deliberately ignores any DSN.

### Offline version selection

Set the target version in `.mig-est.yaml`:

```yaml
mysql:
  version: "8.0.36"
```

The default is `8.0.28`. A series without a patch level, such as `"8.0"` or
`"8.4"`, is treated as the latest patch in that series for feature gates.

## Quick start: online mode

Use a read-only account and point mig-est at a staging database, a replica, or
a shadow database with production-like table sizes. Do not make CI connect
directly to production.

```bash
export MIGEST_DSN='migread:password@tcp(staging-db.example.com:3306)/appdb'

./bin/mig-est check \
  --file ./migrations/20261030_add_status.sql \
  --format markdown \
  --output report.md \
  --fail-on high
```

The DSN is read from `MIGEST_DSN` by default. `--dsn` is available for local
use, but an environment variable avoids leaving the password in shell history.
mig-est never prints the DSN. Reports contain only the host and database name.

Online mode uses `information_schema`, `performance_schema`, and
`SHOW REPLICA STATUS` (falling back to `SHOW SLAVE STATUS` if needed). A denied
metadata source becomes a visible item in the report's **Unknowns** section; it
does not silently become "safe".

### Read-only account

```sql
CREATE USER 'migread'@'%' IDENTIFIED BY 'use-a-strong-password';
GRANT SELECT ON appdb.* TO 'migread'@'%';
-- information_schema is visible through object privileges. MySQL 8.0 rejects
-- a direct GRANT on information_schema.*, so do not add one here.
GRANT SELECT ON performance_schema.* TO 'migread'@'%';
-- PROCESS is needed to see transactions owned by other sessions.
GRANT PROCESS ON *.* TO 'migread'@'%';
-- Optional, for replica status:
GRANT REPLICATION CLIENT ON *.* TO 'migread'@'%';
FLUSH PRIVILEGES;
```

On managed services (Aurora, RDS, Cloud SQL, and similar), access to
`performance_schema` and replica status varies. Missing checks are reported as
unknowns rather than treated as passing checks.

## Command reference

```text
mig-est check   [flags] [migration.sql...]
mig-est check   --file migration.sql [flags]
mig-est check   --dir ./migrations [flags]
mig-est check   --stdin [flags]
mig-est record  [flags] [migration.sql...]
mig-est record  --file migration.sql --actual 8m12s --table orders
mig-est version
mig-est help
```

### `check` flags

| Flag | Default | Description |
| --- | --- | --- |
| `--file PATH` | — | SQL file; repeatable and glob-capable (or pass files as positional arguments) |
| `--dir PATH` | — | Directory of SQL files; repeatable |
| `--pattern GLOB` | `*.sql` | Pattern used with `--dir` |
| `--stdin` | false | Read SQL from standard input |
| `--dsn DSN` | `MIGEST_DSN` | MySQL DSN; prefer the environment variable |
| `--offline` | auto | Never connect; use supplied table statistics |
| `--table-size T=SIZE` | — | Offline size, e.g. `orders=8200MB`; repeatable |
| `--table-rows T=ROWS` | — | Offline row count; repeatable |
| `--disk-free SIZE` | — | Free space on the target host, e.g. `50GB` |
| `--format FORMAT` | `text` | `text`, `markdown`, or `json` |
| `--output PATH` | stdout | Write the report to a file; `-` means stdout |
| `--fail-on LEVEL` | `critical` | `low`, `medium`, `high`, `critical`, or `never` |
| `--config PATH` | `./.mig-est.yaml` | Config file; auto-loaded if present |
| `--timeout DURATION` | `10s` | Timeout for each database query |
| `--no-color` | false | Disable ANSI colors |
| `--quiet` | false | One-line summary in text mode |

Sizes accept bytes, `KB`/`KiB`, `MB`/`MiB`, `GB`/`GiB`, and `TB`/`TiB`.
Row counts accept `,` and `_` separators.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Diagnostics completed; risk is below `--fail-on` |
| `1` | Risk is at or above `--fail-on` (the intentional CI gate) |
| `2` | Usage, parse, config, or connection error |

A high-risk diagnosis is not a tool failure. Exit code `1` is how the CI gate
reports a policy failure, while `2` means mig-est could not produce a diagnosis.

### Risk levels

| Level | Meaning |
| --- | --- |
| `low` | Metadata-only change, no known blocker |
| `medium` | INPLACE work, validation scan, or incomplete metadata |
| `high` | COPY/rebuild, unsupported engine, or a large-table operation |
| `critical` | Disk shortage, MDL blocker, or a statement MySQL is expected to reject |

The overall risk is the highest non-info finding or statement risk.

## Examples

### A safe-looking ALTER that is not always INSTANT

```sql
ALTER TABLE orders
  ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'new';
```

On MySQL 8.0.12+ this is usually INSTANT, but a FULLTEXT index,
`ROW_FORMAT=COMPRESSED`, an unsupported position, or missing table metadata
changes the conclusion. mig-est states the assumptions and raises risk when
metadata is incomplete.

### An expensive type change

```sql
ALTER TABLE orders MODIFY COLUMN status VARCHAR(30);
```

The classifier compares the catalog's `varchar(20)` to `varchar(30)`. Widening
is INPLACE; narrowing, signedness changes, and incompatible families are COPY
or UNKNOWN. A COPY result includes disk requirements and an expand/contract
suggestion.

### Multiple operations in one statement

```sql
ALTER TABLE orders
  ADD COLUMN note TEXT,
  ADD PRIMARY KEY (id);
```

The whole statement inherits the most expensive operation (COPY here), and
mig-est recommends splitting the cheap and expensive work.

### Explicit algorithm expectations

```sql
ALTER TABLE orders
  ADD COLUMN note TEXT,
  ALGORITHM=INSTANT, LOCK=NONE;
```

Explicit expectations make MySQL fail instead of silently choosing a slower
path. mig-est validates the requested combination and flags a statement that
MySQL would reject.

## Configuration

Copy [`.mig-est.yaml.example`](.mig-est.yaml.example) to `.mig-est.yaml`.
Every field is optional.

```yaml
mysql:
  version: "8.0.36"

thresholds:
  large_table_rows: 10000000
  large_table_size_mb: 5000
  long_transaction_minutes: 5
  replica_lag_seconds: 10
  disk_safety_factor: 1.5

fail_on: critical
output:
  format: text

history:
  path: ./.mig-est/history.jsonl
  enabled: true
```

CLI flags override the corresponding config values. The config is searched in
explicit `--config`, then `./.mig-est.yaml` in the current directory.

`--disk-free` must be supplied by the operator; mig-est does not guess host
disk capacity from SQL. Without it, COPY disk safety is reported as unknown.

## Calibration history

Record real timings after a migration and future estimates can use the median
observed speed for the same table and algorithm.

```bash
./bin/mig-est record \
  --file ./migrations/20261030_add_status.sql \
  --actual 8m12s \
  --table orders \
  --size 8200MB \
  --host staging-db.example.com \
  --database appdb
```

The file is JSON Lines at `history.path`. It stores the timestamp, table,
operation, algorithm, row count, size, actual seconds, host, database, and
MySQL version. It does **not** store SQL text, usernames, passwords, or DSNs.

Calibration needs at least three matching records with a positive size and
duration. Until then, estimates stay at `low` confidence and keep a wide range.
Recent records are preferred (the latest 10 by default). Local history may
contain internal host/table names; review it before committing.

## CI example

```yaml
name: migration-check
on:
  pull_request:
    paths:
      - 'migrations/**'

jobs:
  mig-est:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build mig-est
        run: go build -trimpath -o mig-est ./cmd/mig-est
      - name: Check migrations against staging
        env:
          MIGEST_DSN: ${{ secrets.MIGEST_DSN }} # read-only staging account
        run: |
          ./mig-est check --dir ./migrations \
            --format markdown --output mig-est-report.md \
            --fail-on high
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: mig-est-report
          path: mig-est-report.md
```

If the staging table is much smaller than production, estimates will be too
optimistic. Prefer a production-sized shadow database or supply offline sizes
conservatively.

## Local MySQL with Docker

```bash
cd docker
docker compose up -d
```

This starts MySQL 8.0 on port `33080` and MySQL 8.4 on port `33084`, with the
schema in `docker/init/01-schema.sql` and a read-only `migread` user.

```bash
export MIGEST_DSN='migread:migread@tcp(127.0.0.1:33080)/appdb'
./bin/mig-est check --file ./testdata/migrations/02_inplace_index.sql --fail-on high
MIGEST_TEST_DSN="$MIGEST_DSN" go test ./internal/catalog -run Integration -v
```

See [`docker/README.md`](docker/README.md) for details. The credentials are
local-only and must not be reused.

## How it works

1. **Parser** (`internal/parser`): splits statements (including `DELIMITER`
   blocks and comments) and uses Vitess `sqlparser` to extract DDL operations.
2. **Classifier** (`internal/classifier`): combines operation, table metadata,
   MySQL version gates, and explicit `ALGORITHM`/`LOCK` values into a predicted
   algorithm and rule IDs.
3. **Catalog** (`internal/catalog`): read-only `SELECT`/`SHOW` collection from
   `information_schema`, `performance_schema`, and replica status.
4. **Estimator** (`internal/estimator`): size/speed ranges, disk sizing, and
   calibration.
5. **Checker** (`internal/checker`): blockers and context-sensitive findings.
6. **Suggester** (`internal/suggester`): expand/contract, explicit algorithm,
   foreign-key orphan checks, and gh-ost/pt-osc caveats.
7. **Report** (`internal/report`): text, Markdown, and stable JSON.
8. **History** (`internal/history`): append-only JSON Lines calibration.

The parser, classifier, estimator, suggester, and report renderers can run
without a database connection. This is what makes offline CI checks possible.

## Project layout

```text
cmd/mig-est/          CLI entry point
internal/cli/         commands, flags, input gathering, exit codes
internal/parser/      SQL splitter and Vitess AST extraction
internal/classifier/  algorithm matrix, version gates, type compatibility
internal/catalog/     read-only MySQL metadata collection
internal/estimator/   time/disk estimates and calibration
internal/checker/     blockers and metadata-dependent findings
internal/suggester/   safe alternatives
internal/report/      text, Markdown, JSON renderers
internal/history/     JSON Lines calibration history
internal/model/       shared types
internal/mysqlver/    version parsing and feature gates
testdata/             parser/classifier fixtures and expected results
docker/               local MySQL 8.0/8.4 environment
docs/spec.md          v0.1 specification
```

## Benchmarks

These are single-run measurements from local Docker MySQL containers. The test
table used InnoDB, an auto-increment BIGINT primary key, and a 64-character
payload. Each row count was verified after the ALTER. The measured statement
converted the table from `utf8mb4_0900_ai_ci` to `utf8mb4_bin` with
`ALGORITHM=COPY, LOCK=EXCLUSIVE`.

| MySQL | Rows | Table size | mig-est estimate (low confidence) | Measured wall time |
| --- | ---: | ---: | ---: | ---: |
| 8.0.46 | 1,000,000 | 99,221,504 bytes (94.6 MiB) | 0.6–1.9 s | 1.99 s |
| 8.0.46 | 10,000,000 | 988,807,168 bytes (943.0 MiB) | 6.3–18.9 s | 21.18 s |
| 8.4.11 | 1,000,000 | 99,221,504 bytes (94.6 MiB) | 0.6–1.9 s | 1.52 s |
| 8.4.11 | 10,000,000 | 988,807,168 bytes (943.0 MiB) | 6.3–18.9 s | 12.70 s |

All four operations completed using the requested COPY algorithm and EXCLUSIVE
lock. The 8.0 measurements were slightly above the estimate's upper bound; the
8.4 measurements were within range. These are exploratory results, not a
comparison of MySQL versions or a performance guarantee. Each case was run
once, sequentially, without explicitly clearing OS or InnoDB caches. The
container engine had 8 CPUs and 4,106,604,544 bytes of memory; each server used
a 128 MiB InnoDB buffer pool. The host-measured duration includes `docker exec`
and MySQL client startup. See [`docs/spec.md`](docs/spec.md#173-performance-benchmark-record)
for the test setup and additional context.

## Known limitations

- This is a prototype; release binaries and Homebrew packaging are not
  published.
- The classification matrix is intentionally conservative. It is not a
  substitute for testing the exact statement on a production-sized staging
  copy.
- `information_schema.TABLE_ROWS` is an InnoDB estimate. mig-est does not run
  `COUNT(*)` and displays row counts as approximate online.
- Type compatibility covers common integer, string, decimal, TEXT/BLOB, and
  ENUM/SET changes; unknown pairs return `UNKNOWN`.
- Partition and tablespace operations are conservative and deserve manual
  review.
- gh-ost/pt-osc advice is a prerequisite check, not an execution integration.
- The published benchmarks are single-run local Docker results and may not
  represent production hardware. Repeated tests on production-sized staging
  systems are still needed. Prebuilt Linux/macOS release assets and Homebrew
  packaging are not published.
- The tool does not inspect table data and does not check PII or business
  constraints in existing rows.

## Safety

- mig-est sends only `SELECT` and `SHOW` to MySQL. It does not issue DDL or DML.
- Use a read-only account and prefer a staging/replica database.
- The DSN is never written to reports, logs, or history. Hosts are shown without
  credentials.
- SQL text is never written to history.
- Reports can contain schema details and host names; review them before sharing.
- If a check cannot collect a field, it reports an unknown and stays
  conservative instead of silently lowering risk.

## Contributing

Run `make all` before sending a change. Add parser/classifier fixtures for new
DDL forms and keep `UNKNOWN` conservative: when in doubt, do not claim
`INSTANT`.

The project uses MIT licensing. See [`LICENSE`](LICENSE).
