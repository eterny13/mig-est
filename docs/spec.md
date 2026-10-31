# mig-est v0.1 Specification

- Project: mig-est
- Target databases: MySQL 8.0 and 8.4 with InnoDB
- Created: 2026-10-30
- Status: v0.1 release specification. Exploratory single-run performance measurements are recorded in Section 17.3 and summarized in README.
- Summary: A CLI that diagnoses a migration before it is applied, predicting its algorithm, blockers, approximate duration, disk needs, and safer alternatives without executing the migration.

---

## 0. Decisions

| Area | Decision |
|---|---|
| Databases in v0.1 | MySQL 8.0 and 8.4, InnoDB only. MariaDB, MySQL 5.7, and PostgreSQL are out of scope. |
| Language | Go |
| Form | Standalone CLI binary; no server, daemon, or SaaS |
| Safety | Diagnostics only. The tool never executes migrations. |
| Connection | Use a read-only account. Offline mode accepts table statistics when database access is unavailable. |
| Initial distribution | Linux and macOS binaries through GitHub Releases. Homebrew may be considered for v0.2. |
| License | MIT; see the repository LICENSE file. |

## 1. Background

MySQL ALTER TABLE statements can have very different costs. Some change metadata in milliseconds; others rebuild a table for minutes or hours. Online DDL can still wait for a metadata lock (MDL), and a waiting ALTER can cause later queries to queue behind it. A table rebuild can also fail when the host lacks disk space.

mig-est is intended to make these risks visible before deployment.

| Tool category | Role | How mig-est differs |
|---|---|---|
| Flyway, Alembic, goose, and similar tools | Apply and manage migrations | mig-est diagnoses migrations but does not apply them. |
| gh-ost and pt-online-schema-change | Apply schema changes online | mig-est predicts when native DDL may suffice and when an online migration tool may need consideration. It does not launch those tools. |
| MySQL Online DDL reference tables | Document server behavior | mig-est applies rules to the SQL, server version, and available table metadata. |

The intended role is similar to a plan command for MySQL migrations: a review aid and a possible CI gate.

## 2. Goals and non-goals

### 2.1 Goals

1. Predict the expected MySQL algorithm: INSTANT, INPLACE, or COPY.
2. Assess risk using table size, row count, indexes, foreign keys, triggers, and partitions.
3. Detect observable blockers such as long transactions, MDL waits, replica lag, and insufficient disk space.
4. Estimate duration as a range with a confidence level, and improve estimates with calibration data.
5. Suggest concrete safer alternatives for risky operations.
6. Provide JSON output and exit codes suitable for CI.

### 2.2 Non-goals for v0.1

- Applying, rolling back, or rewriting migrations; launching gh-ost or pt-online-schema-change
- Real-time monitoring, dashboards, or notifications
- PostgreSQL, MariaDB, or MySQL 5.7 support
- Inspecting row contents or business data
- Claiming an exact migration duration; actual time depends on hardware, I/O, and concurrent load

## 3. Users and use cases

### 3.1 Users

- Application developers reviewing migrations in pull requests
- Reviewers and DBAs assessing whether a migration can run during business hours
- SREs adding a migration gate to CI

### 3.2 Use cases

1. Check migration files locally during development.
2. Check pull requests in CI and fail at a configured risk level.
3. Inspect blockers immediately before applying a migration.
4. Record completed migration durations to improve later estimates.

## 4. Architecture

~~~
Migration SQL files
        |
        v
Parser (Vitess SQL parser) -> Extract DDL operations
        |
        v
Classifier (operation, table attributes, MySQL version)
        |
        +---- Catalog collector <----> MySQL read-only metadata
        |
        v
Checker and estimator (risk, blockers, duration range, disk)
        |
        v
Text / Markdown / JSON report and exit code 0, 1, or 2
~~~

Design principles:

- Classification works without a database connection and can report the operation and theoretical algorithm from SQL alone.
- Failure to read a metadata item does not abort the whole check. The report marks it unknown and uses conservative risk.
- Estimates are ranges. Without calibration, confidence is low and ranges are wide.
- Findings include rule IDs such as COPY-01 and MDL-01 so users can trace why a risk was reported.

## 5. CLI

### 5.1 Commands

| Command | Purpose |
|---|---|
| mig-est check | Diagnose migrations |
| mig-est record | Record an applied migration's actual duration for calibration |
| mig-est version | Print the tool and supported MySQL versions |

Commands such as explain, diff, and init are outside v0.1.

### 5.2 Check examples

~~~
# Set a read-only connection string outside shell history.
export MIGEST_DSN='migread:password@tcp(db.example.com:3306)/appdb'

# Positional files, --file, and their interleaving are supported.
mig-est check migrations/01_add_status.sql --dsn "$MIGEST_DSN"
mig-est check --file migrations/01_add_status.sql --dsn "$MIGEST_DSN"
mig-est check migrations/01_add_status.sql --file migrations/02_add_index.sql

# A directory is read in filename order.
mig-est check --dir ./migrations --pattern '*.sql'

# Offline mode with manually supplied table statistics.
mig-est check migrations/01_add_status.sql --offline \
  --table-size orders=8200MB --table-rows orders=12400000

# JSON output and a CI gate.
mig-est check --file migrations/01_add_status.sql \
  --format json --fail-on high --output report.json

# Read SQL from standard input.
cat migration.sql | mig-est check --stdin
~~~

Positional files cannot be combined with --stdin or --dir. Those combinations are usage errors and return exit code 2.

### 5.3 Flags

| Flag | Required | Default | Description |
|---|---|---|---|
| --file PATH | One input source is required | None | Repeatable SQL file path; glob patterns are supported. |
| Positional files | One input source is required | None | One or more SQL files; can be intermixed with flags and --file. |
| --dir PATH | One input source is required | None | Read SQL files from a directory in filename order. Repeatable. |
| --stdin | One input source is required | False | Read SQL from standard input. |
| --pattern GLOB | No | *.sql | Pattern used with --dir. |
| --dsn DSN | No | MIGEST_DSN | MySQL DSN. Not required in offline mode. |
| --offline | No | False / automatic when no DSN is configured | Do not connect; use supplied table statistics. |
| --table-size TABLE=SIZE | No | None | Repeatable size input, for example orders=8200MB. |
| --table-rows TABLE=ROWS | No | None | Repeatable row-count input. |
| --disk-free SIZE | No | None | Available disk space, for example 50GB. Unknown when omitted. |
| --format FORMAT | No | text | text, markdown, or json. |
| --output PATH | No | Standard output | Write the report to a file. |
| --fail-on LEVEL | No | critical | Exit 1 at or above low, medium, high, or critical; never disables the risk gate. |
| --config PATH | No | ./.mig-est.yaml when present | Configuration file. |
| --timeout DURATION | No | 10s | Timeout for each database query. |
| --no-color | No | False | Disable ANSI colors. |
| --quiet | No | False | Print a one-line summary in text mode. |

DSN handling:

- Prefer MIGEST_DSN to avoid storing a password in shell history.
- Never print a DSN or password in reports, logs, or errors. Mask displayed connection details.
- Do not put a password-bearing DSN in help examples.

### 5.4 Exit codes

| Code | Meaning |
|---|---|
| 0 | Diagnosis completed below the configured --fail-on threshold. |
| 1 | Risk is at or above --fail-on; intended for failing CI. |
| 2 | Usage, SQL parsing, connection, or configuration error. |

A HIGH finding is a completed diagnosis, not a tool error. It returns 1 only when the configured gate requires it.

### 5.5 Configuration

Example .mig-est.yaml:

~~~yaml
mysql:
  version: "8.0" # In online mode, the server version is detected.
thresholds:
  large_table_rows: 10000000
  large_table_size_mb: 5000
  long_transaction_minutes: 5
  replica_lag_seconds: 10
  disk_safety_factor: 1.5
fail_on: high
output:
  format: markdown
history:
  path: ./.mig-est/history.jsonl
  enabled: true
~~~

All settings are optional and use defaults when omitted.

## 6. Input and statement handling

### 6.1 SQL files

- A file may contain multiple statements; each is diagnosed separately.
- SQL comments are handled by the parser and are not treated as statements.
- UTF-8 is expected.
- DELIMITER commands, stored programs, and trigger definitions are unsupported in v0.1 and must not be reported as safe.

### 6.2 Supported statement types

- ALTER TABLE
- CREATE [UNIQUE | FULLTEXT | SPATIAL] INDEX
- DROP INDEX
- CREATE TABLE, classification only; no size estimate
- DROP TABLE, with a warning when referenced
- RENAME TABLE, reported as a metadata operation

Other statements, including INSERT, UPDATE, and CREATE VIEW, are out of scope and excluded from the risk score.

### 6.3 Multi-operation ALTER

Classify each operation within an ALTER statement. The statement takes the highest risk among its operations. For example, combining an INSTANT ADD COLUMN with a COPY ADD PRIMARY KEY makes the whole statement COPY-risk. Report the expensive operation and recommend separating it.

### 6.4 Explicit ALGORITHM and LOCK clauses

When SQL specifies ALGORITHM or LOCK:

- Check whether the requested combination is supported and report if MySQL is expected to reject it.
- When omitted, predict the algorithm MySQL is expected to choose.
- Recommend explicit clauses such as ALGORITHM=INSTANT, LOCK=NONE when suitable, so an unexpected fallback fails instead of silently becoming more expensive.

## 7. Classification engine

### 7.1 Classification inputs and results

Classification considers the operation, table attributes, MySQL version, and explicit ALGORITHM / LOCK clauses. Each result carries rule IDs.

| Result | Meaning |
|---|---|
| INSTANT | Metadata-only operation; does not rewrite rows. |
| INPLACE | No full table copy, although index construction or row-format work may take time. |
| COPY | Rebuilds the table into a replacement table; generally the most expensive path. |
| UNKNOWN | Unsupported rule or insufficient metadata. Requires manual review and at least MEDIUM risk; never means safe. |

### 7.2 Representative v0.1 matrix

These are representative rules, not universal guarantees. Always check the target MySQL version and table conditions.

| Operation | Expected algorithm | Notes |
|---|---|---|
| ADD COLUMN at end, MySQL 8.0.12+ | INSTANT | From 8.0.29, supported positions include FIRST and AFTER. Exclusions include COMPRESSED row format, FULLTEXT indexes, data-dictionary tablespaces, and temporary tables. |
| ADD COLUMN at a non-final position before 8.0.29 | INPLACE | Position affects INSTANT support. |
| DROP COLUMN | INSTANT on 8.0.29+; otherwise INPLACE | Instant drop marks the column deleted internally; physical space may remain until a rebuild. |
| Add secondary index | Usually INPLACE; LOCK=NONE is often available | Sorting and index construction may take time. FULLTEXT and SPATIAL need separate handling. |
| DROP INDEX | INPLACE | Usually lighter, but still needs MDL. |
| ADD PRIMARY KEY | COPY | Reorders existing data and can be expensive. |
| DROP PRIMARY KEY | Often COPY | Verify the exact operation and table. |
| MODIFY / CHANGE COLUMN | Depends on type compatibility | Compatible widening may be INPLACE; incompatible conversions may use COPY. See the type-pair rules. |
| SET / DROP DEFAULT | Usually INSTANT | Usually metadata-only. |
| SET / DROP NOT NULL | Often INPLACE | Existing rows may need validation; NULL values can make the change fail. |
| ADD FOREIGN KEY | INPLACE plus validation | Existing rows are scanned for referential integrity. |
| DROP FOREIGN KEY | Often INPLACE | Verify the exact operation. |
| ADD CHECK constraint | INPLACE plus validation | Existing rows need validation. |
| RENAME COLUMN | INSTANT on MySQL 8.0+ | Renames metadata. |
| RENAME TABLE | Metadata operation | Requires a brief MDL; does not rebuild the table. |
| Convert character set or collation | COPY | Rewrites rows. |
| Change storage engine | COPY | Rebuilds the table. |
| Add or drop partition | Operation-dependent; UNKNOWN-biased in v0.1 | Require manual verification. |
| Change ROW_FORMAT | COPY | Rebuilds the table. |

Rules are implemented as testable code and fixtures, not treated as immutable MySQL guarantees. Update them as server behavior and verification results change.

### 7.3 Type compatibility

- Widening within a type family, such as VARCHAR(50) to VARCHAR(100) or INT to BIGINT, is generally treated as INPLACE-biased, but signedness and precision changes need separate checks.
- Changes between type families, such as VARCHAR to INT or DATE to VARCHAR, are COPY-biased.
- ENUM / SET element additions, removals, and reordering are conservative. Removal or reordering can change data meaning and should raise at least HIGH risk.
- Uncovered pairs are UNKNOWN and require staging verification.

### 7.4 Version handling

- Online mode reads SELECT VERSION().
- Offline mode uses mysql.version. If omitted, use a conservative MySQL 8.0 assumption equivalent to a version before 8.0.29.
- Where a rule varies by version, include the condition in the report.

## 8. Read-only metadata collection

### 8.1 Principles

- Issue SELECT and SHOW queries only. Never issue DDL or DML.
- Apply a per-query timeout (10 seconds by default).
- If a query fails or permission is missing, mark that item unknown, continue, and explain which metadata was unavailable.

### 8.2 Metadata sources

| Data | Representative source | Use |
|---|---|---|
| Rows, data bytes, index bytes | information_schema.tables | Estimates; TABLE_ROWS is approximate for InnoDB. |
| Columns, types, nullability, defaults | information_schema.columns | Type compatibility and column position. |
| Indexes | information_schema.statistics | Duplicate-index checks and build cost. |
| Foreign keys | information_schema.referential_constraints and key_column_usage | Impact of column and key changes. |
| Triggers | information_schema.triggers | Online migration tool constraints and COPY warnings. |
| Partitions | information_schema.partitions | Conservative partition handling. |
| Table options | SHOW CREATE TABLE | Engine, row format, and character set. |
| Transactions | information_schema.innodb_trx and processlist | Long-running and idle transactions. |
| Metadata locks | performance_schema.metadata_locks and threads | MDL blockers; report unavailable if performance_schema cannot be read. |
| Replication | SHOW REPLICA STATUS or legacy SHOW SLAVE STATUS | Replica lag and stopped replication; skip when not a replica. |
| Version and variables | SELECT VERSION(), SHOW VARIABLES | Version rules and online alter log limits. |

### 8.3 InnoDB row estimates

information_schema.tables.TABLE_ROWS is an estimate for InnoDB. Reports should label it approximate and use ranges. Do not run SELECT COUNT(*) on large tables in v0.1. An exact-count option may be considered later.

## 9. Risk levels and blockers

### 9.1 Risk levels

| Level | Meaning | Default CI behavior (--fail-on critical) |
|---|---|---|
| LOW | INSTANT-like operation with no observed blockers; likely suitable for normal hours. | Pass |
| MEDIUM | INPLACE work or validation scan; consider load and timing. | Warn; fail when threshold is medium or lower. |
| HIGH | COPY-like or otherwise heavy work on a large table; consider a maintenance window or online migration tool. | Fail when threshold is high or lower. |
| CRITICAL | Disk shortage, MDL blocker, or no viable online path, among other stop conditions. | Fail by default |

The statement risk is the highest risk among its operations. A CRITICAL blocker makes the overall result CRITICAL even for an INSTANT operation.

### 9.2 Blocker checks

| ID | Check | Default condition | Guidance |
|---|---|---|---|
| MDL-01 | Long transaction | Transaction touching the table has run for 5 minutes or more. | Identify its owner; wait for completion or postpone the migration. |
| MDL-02 | Waiting metadata lock | A session is waiting for MDL on the table. | Resolve the cause before applying the migration. |
| MDL-03 | Idle transaction | A sleeping connection has an open transaction. | Investigate application commit handling; do not kill it blindly. |
| REP-01 | Replica lag | Lag is 10 seconds or more. | Wait for lag to clear. Online DDL may increase it. |
| REP-02 | Replica stopped | SQL or I/O replication thread is stopped. | Resolve replication before proceeding. |
| FK-01 | Referenced table | Other tables reference the target. | Check impact of type or primary-key changes. |
| FK-02 | Referencing table | Target references other tables. | Check validation and dependency scope. |
| TRG-01 | Trigger exists | Target table has triggers. | gh-ost cannot operate on tables with triggers; evaluate alternatives. |
| PK-01 | No primary key | Target table has no primary key. | gh-ost / pt-online-schema-change may require a primary or unique key. |
| PART-01 | Partitioned table | Target is partitioned. | Verify the exact operation on staging. |
| DISK-01 | Insufficient or unknown disk | COPY estimate exceeds --disk-free, or disk capacity is not provided. | Free disk or evaluate another approach. Unknown capacity is conservative and at least MEDIUM risk. |
| SIZE-01 | Heavy work on a large table | COPY / rebuild on at least 10M rows or 5 GB. | Consider off-hours or an online migration tool. |
| FTS-01 | FULLTEXT index | ADD / DROP COLUMN on a table with FULLTEXT. | INSTANT may be unavailable; check version and operation. |
| ROWFMT-01 | COMPRESSED row format | INSTANT ADD COLUMN on a COMPRESSED table. | INSTANT is unavailable; verify the fallback. |
| LOG-01 | Online alter log limit | Write activity may exceed the online alter log (128 MiB by default). | Prefer a lower-write period. v0.1 can only give a heuristic warning. |

ALTER needs an exclusive metadata lock at least briefly and may wait for transactions holding shared metadata locks. Queries behind a waiting ALTER may also queue, so MDL risk is separate from operation duration.

### 9.3 Online migration tool constraints

Do not recommend gh-ost or pt-online-schema-change without checking prerequisites:

- A primary or unique key may be required.
- gh-ost cannot use a table that has triggers.
- Foreign keys referencing the parent table may constrain gh-ost.
- Replication topology and row-based replication requirements must be checked.

When prerequisites fail, say so and consider an expand/contract migration: add a new column, update the application, then remove the old column in a later migration.

## 10. Estimate model

### 10.1 Principles

- Always report a minimum and maximum with high, medium, or low confidence.
- With no calibration data, use low confidence and a wide range.
- Estimate operation work, not MDL wait time. Report MDL separately.
- Use readable units in text and seconds in JSON.

### 10.2 v0.1 formulas

~~~
COPY:
  Copy bytes = DATA_LENGTH + INDEX_LENGTH
  Estimated time = copy bytes / assumed copy throughput
  Initial conservative throughput range: 50-150 MB/s.
  Replace the default with environment-specific calibrated throughput when enough data exists.

INPLACE index build:
  Estimate index bytes from row count and average key size, then divide by
  an assumed sort/build throughput. Use a wide initial range.

INSTANT:
  Assume under one second when MDL is available. State that MDL wait can add time.

Validation scans:
  Estimate table bytes / assumed full-scan throughput.
~~~

Illustration only: copying 8.2 GB at 100 MB/s is about 82 seconds; at 50 MB/s it is about 164 seconds. These are not benchmark measurements or guaranteed defaults.

### 10.3 Disk estimates

| Operation | Approximate free space needed |
|---|---|
| COPY | (DATA_LENGTH + INDEX_LENGTH) x 1.5 for the old table, new table, and temporary files. |
| INPLACE index addition | New index plus temporary sort space. |
| INSTANT | Minimal; allow additional room for metadata and binary logs. |
| gh-ost | A ghost table comparable to the original plus binary log growth. |

Without --disk-free, report disk capacity as unknown. Do not infer host disk capacity from SQL metadata.

### 10.4 Calibration

- mig-est record stores actual duration, table, operation, and size.
- Store one record per line in .mig-est/history.jsonl.
- Use the median of the latest 10 records for the same host/database and algorithm.
- Fewer than three records keeps confidence low and does not narrow the range.
- Calibration is environment-specific; do not directly reuse throughput from another host.

Example:

~~~sh
mig-est record --file ./migrations/20261030_add_status.sql \
  --actual 8m12s --table orders
~~~

A record may include timestamp, table, algorithm, row count, size, duration, host, and database. It must not contain SQL text, passwords, or DSNs.

## 11. Safer alternatives

Suggestions are guidance, not automatic SQL rewrites. Users decide what to apply and verify it on staging.

| Operation | Risk | Suggestion |
|---|---|---|
| CREATE INDEX on a large table | INPLACE can still consume time and write capacity. | Specify ALGORITHM=INPLACE, LOCK=NONE when supported; consider a low-traffic window or an online migration tool. |
| Type change requiring COPY | Full table rebuild and long runtime. | Check for a compatible conversion; otherwise use expand/contract or a supported online migration tool. |
| Add NOT NULL column without a default | Backfill or validation may be expensive. | Add nullable, backfill in batches, then apply NOT NULL and a default. |
| Add foreign key | Scans existing rows. | Check for orphan rows first; explain that MySQL has no PostgreSQL NOT VALID equivalent. |
| Add primary key | Often COPY. | Confirm the need and whether an online migration tool's key prerequisites are met. |
| Convert character set | Rewrites rows and may have irreversible effects. | Validate all data on staging; consider an online migration tool if its constraints are satisfied. |
| Multiple operations in one ALTER | The heaviest operation dominates. | Split lightweight and expensive operations into separate statements. |
| No explicit ALGORITHM / LOCK | Server may choose a more expensive fallback. | Specify expected clauses when supported so unexpected fallback fails. |

Include suggested SQL in code blocks and remind users to test it on staging.

## 12. Reports

### 12.1 Text output

Text output should show target table, approximate row count and size, server version, each statement's prediction, risk, estimate, findings, suggestions, overall risk, and exit code.

Display HIGH in red, MEDIUM in yellow, and LOW in green. --no-color disables colors. Use SQL summaries plus file and line references instead of printing long statements. Never hide unknown values.

### 12.2 Markdown output

Markdown is intended for pull requests and CI artifacts. It should contain information equivalent to text output and may be written with --output report.md.

### 12.3 JSON output

JSON is the canonical machine-readable format. Adding fields is compatible; removing fields or changing field types requires a major version.

Example excerpt:

~~~json
{
  "tool": "mig-est",
  "version": "0.1.0",
  "mysql": {
    "version": "8.0.36",
    "database": "appdb",
    "host_masked": "db.example.com:3306"
  },
  "overall_risk": "high",
  "statements": [
    {
      "index": 1,
      "file": "20261030_add_status.sql",
      "line": 1,
      "sql_summary": "ALTER TABLE orders ADD COLUMN status VARCHAR(20)",
      "table": "orders",
      "operation": "ADD_COLUMN",
      "predicted_algorithm": "INPLACE",
      "lock": "SHARED",
      "risk": "medium",
      "rule_ids": ["ADD-COL-02"],
      "estimate": {
        "min_seconds": 60,
        "max_seconds": 240,
        "confidence": "low"
      }
    }
  ],
  "blockers": [],
  "unknowns": ["disk_free: not provided"]
}
~~~

## 13. Security and safety

### 13.1 Read-only user

Document a least-privilege account for production connections. Exact grants vary by MySQL and managed service:

~~~sql
CREATE USER 'migread'@'%' IDENTIFIED BY 'use-a-strong-password';
GRANT SELECT ON appdb.* TO 'migread'@'%';
GRANT SELECT ON performance_schema.* TO 'migread'@'%';
-- Grant replication status access only when needed and supported.
~~~

Do not grant write permissions. If performance_schema is restricted, report affected checks as unknown. Managed services such as Aurora, RDS, and Cloud SQL may require different permissions.

### 13.2 Never execute migrations

- The v0.1 program must not send DDL or DML to a database.
- Integration tests that write to a database must use a local Docker database, not production.
- Any future execution feature must be an explicit separate command and must never execute by default.

### 13.3 Secrets

- Do not write DSNs, passwords, or credentials into reports, logs, JSON, or errors.
- Mask host details in output.
- History may include a host identifier but must not store passwords or usernames. Users should consider whether hostnames are sensitive before committing history.

## 14. Configuration, history, and CI

### 14.1 Config lookup

1. Explicit --config path
2. ./.mig-est.yaml in the current directory
3. Git-root search is a possible later enhancement; v0.1 checks the current directory only.

The tool works with defaults when no config file exists.

### 14.2 History

- Path: history.path, default ./.mig-est/history.jsonl
- Format: JSON Lines, one applied migration per line
- May store timestamp, table, operation, algorithm, row count, size, actual duration, masked host, and MySQL version
- Do not store SQL text, credentials, or DSNs

### 14.3 GitHub Actions example

~~~yaml
name: migration-check
on:
  pull_request:
    paths:
      - migrations/**

jobs:
  mig-est:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install mig-est
        run: |
          curl -fsSL -o mig-est https://github.com/OWNER/mig-est/releases/download/v0.1.0/mig-est-linux-amd64
          chmod +x mig-est
      - name: Check migrations
        env:
          MIGEST_DSN: secret-read-only-staging-dsn
        run: |
          ./mig-est check --dir ./migrations \
            --format markdown --output mig-est-report.md \
            --fail-on high
      - name: Upload report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: mig-est-report
          path: mig-est-report.md
~~~

CI must not connect directly to production. Use staging or a production-sized shadow database. Estimates from a smaller staging database may be low. v0.1 stores a report artifact; automatic pull-request comments are later work.

## 15. Go project layout

The implementation is organized into cmd/mig-est for the binary entry point and internal packages for CLI, parser, classifier, catalog, estimator, checker, suggester, report, history, configuration, models, and MySQL version handling. Test fixtures live under testdata/migrations and testdata/expected. Docker Compose provides local MySQL 8.0 and 8.4 instances.

Key dependencies include go-sql-driver/mysql, Vitess sqlparser, and yaml.v3. The MySQL driver and SQL parser should not be reimplemented. Other dependencies should be added only when they reduce meaningful bug risk.

## 16. Language choice

Go is selected because it provides straightforward single-binary distribution and cross-compilation, a mature MySQL driver, and the Vitess MySQL parser. It also shares an ecosystem with gh-ost. Rust offers strong safety and performance but a less mature relevant ecosystem; Python is fast for prototypes but less convenient for standalone distribution; TypeScript/Bun and Ruby/PHP are alternatives with trade-offs in parser maturity and packaging. Runtime performance is not the main requirement for v0.1.

Use the supported Go version stated in README. Use gofmt and go vet in CI. GoReleaser may be considered for automating release artifacts and checksums.

## 17. Verification, benchmarks, and v0.1 completion

### 17.1 Test types

- Classifier golden tests for representative SQL and type pairs
- Estimator unit tests for range calculations
- Docker integration tests for metadata collection and blockers on MySQL 8.0 and 8.4
- MDL integration tests with a separate long-running transaction
- Performance benchmarks on one-million-row and ten-million-row tables, comparing predictions with actual ALTER duration

### 17.2 Recorded functional verification

The following functional checks were reported for MySQL 8.0.46 and 8.4.11. They verify classification and online metadata behavior; they are not large-table performance benchmarks.

| Target | Online read-only integration | Online SQL checks |
|---|---|---|
| MySQL 8.0.46, 127.0.0.1:33080 | TestIntegrationOnlineReadOnly: PASS | 07_add_foreign_key.sql and 01_instant_add_column.sql: verified |
| MySQL 8.4.11, 127.0.0.1:33084 | TestIntegrationOnlineReadOnly: PASS | 07_add_foreign_key.sql and 01_instant_add_column.sql: verified |

Reported findings:

- 07_add_foreign_key.sql: FK-ADD-01 predicted INPLACE with LOCK=SHARED; the report suggested checking for orphan rows. FK-02 was deduplicated to one table finding.
- 01_instant_add_column.sql: ADD-COL-01 predicted INSTANT with LOCK=METADATA. FK-02 was downgraded to INFO because no foreign-key or primary-key columns changed. No unrelated blocker noise was reported.
- The project reports go vet ./..., go test -race ./..., and make smoke as passing. Re-run these on the final release commit, including the positional-file ordering regression test.

### 17.3 Performance benchmark record

The following exploratory benchmark compares mig-est's online prediction with one measured ALTER per row count and server version. Each test used a disposable local Docker database and a table with an auto-increment BIGINT primary key and a 64-character payload. Exactly 1,000,000 or 10,000,000 rows were inserted using a cross join of six or seven ten-row digit tables. The source table used InnoDB, utf8mb4, and utf8mb4_0900_ai_ci.

The dataset was generated with this pattern:

~~~sql
CREATE TABLE digits (d TINYINT UNSIGNED NOT NULL PRIMARY KEY) ENGINE=InnoDB;
INSERT INTO digits VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);
CREATE TABLE bench_1m (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  payload VARCHAR(64) NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE bench_10m LIKE bench_1m;

INSERT INTO bench_1m (payload)
SELECT RPAD('x',64,'x') FROM digits a CROSS JOIN digits b CROSS JOIN digits c
  CROSS JOIN digits d CROSS JOIN digits e CROSS JOIN digits f;
INSERT INTO bench_10m (payload)
SELECT RPAD('x',64,'x') FROM digits a CROSS JOIN digits b CROSS JOIN digits c
  CROSS JOIN digits d CROSS JOIN digits e CROSS JOIN digits f CROSS JOIN digits g;
~~~

Before each ALTER, mig-est checked the table online through the local read-only account. The tested statement explicitly requested COPY and EXCLUSIVE locking:

~~~sql
ALTER TABLE bench_1m CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  ALGORITHM=COPY, LOCK=EXCLUSIVE;
~~~

The 10M-row case used bench_10m with the same statement. Each ALTER was run once, sequentially, and timed from the host around docker exec and the mysql client. The timings therefore include client/container invocation overhead. No OS or InnoDB cache was explicitly cleared. Treat these as exploratory results for this environment, not a comparison of MySQL versions or a general performance guarantee.

| MySQL version | Rows | Operation and SQL | Table size | Actual duration | mig-est range / confidence | Algorithm observed |
|---|---:|---|---:|---:|---|---|
| 8.0.46 | 1,000,000 | CONVERT TO CHARACTER SET | 99,221,504 bytes (94.6 MiB) | 1.99 s | 0.6–1.9 s / low | COPY, EXCLUSIVE |
| 8.0.46 | 10,000,000 | CONVERT TO CHARACTER SET | 988,807,168 bytes (943.0 MiB) | 21.18 s | 6.3–18.9 s / low | COPY, EXCLUSIVE |
| 8.4.11 | 1,000,000 | CONVERT TO CHARACTER SET | 99,221,504 bytes (94.6 MiB) | 1.52 s | 0.6–1.9 s / low | COPY, EXCLUSIVE |
| 8.4.11 | 10,000,000 | CONVERT TO CHARACTER SET | 988,807,168 bytes (943.0 MiB) | 12.70 s | 6.3–18.9 s / low | COPY, EXCLUSIVE |

The row counts were verified with COUNT(*) after each ALTER. information_schema.TABLE_ROWS returned approximate values of 994,125 and 9,940,920 before the ALTER. mig-est classified all four cases as COPY using rule CHARSET-01. The 8.0 timings were slightly above the estimate's upper bound; both 8.4 timings were within the range. Do not tune default throughput from this single sample.

Environment: Apple Silicon arm64 MacBook Air host; Docker engine allocated 8 CPUs and 4,106,604,544 bytes of memory; each MySQL instance had innodb_buffer_pool_size=134,217,728 bytes, innodb_flush_log_at_trx_commit=1, and sync_binlog=1. The Compose image tags mysql:8.0 and mysql:8.4 resolved to MySQL 8.0.46 and 8.4.11. Table sizes are DATA_LENGTH + INDEX_LENGTH from information_schema.tables before the ALTER.

The benchmark completion criterion also asks for these results in README. The summary and environment are now recorded in both this specification and README; the measurements remain exploratory because each case was run once on a local Docker setup.

### 17.4 Definition of Done

The original v0.1 completion criteria are:

- [ ] Correctly classify at least 20 representative MySQL 8.0 ALTER patterns using golden tests.
- [ ] Read table size, row estimates, foreign keys, triggers, and MDL blockers through a read-only connection.
- [ ] Report risk for COPY, INPLACE, and INSTANT and support CI failure at HIGH or above.
- [ ] Emit a duration range and confidence; use low confidence without calibration.
- [ ] Offer safer alternatives for at least five major risky operations.
- [ ] Support JSON and Markdown output.
- [x] Publish measured one-million-row and ten-million-row Docker benchmark results in README.
- [ ] Document non-execution, read-only account setup, and staging usage in README.
- [ ] Publish Linux and macOS binaries through GitHub Releases.

The performance benchmark item is now documented in this specification and README. The remaining unchecked items still need evidence or an explicit change to the v0.1 release scope before claiming that the full Definition of Done is met.

## 18. Roadmap

- v0.1: MySQL 8.0 / 8.4 InnoDB CLI, classification, blocker checks, range estimates and calibration, JSON/Markdown output, and CI gate.
- v0.2: Pull request comments, Git-root config lookup, Homebrew distribution, and assessment of MariaDB demand.
- v0.3: Consider PostgreSQL, exact row counts, and write-rate estimates.
- v0.4+: Generate (but do not execute) gh-ost / pt-online-schema-change commands, support multi-environment checks, and compare reports over time.

## 19. Risks and open questions

| Risk | Impact | Mitigation |
|---|---|---|
| Parser gaps or large Vitess dependency | Classification accuracy and binary size | Maintain parser fixtures; assess alternatives only if needed. |
| MySQL version behavior changes | Misclassification | Keep version conditions explicit and verify against both supported series. |
| TABLE_ROWS is approximate | Wider estimate uncertainty | Show ranges and confidence; do not run COUNT(*) on large tables. |
| Host free disk is unavailable through SQL | Disk check may be unknown | Accept --disk-free input; never guess. |
| performance_schema access varies | Some MDL checks are unavailable | Report missing data and use available process-list checks where possible. |
| Managed MySQL permissions differ | Some checks may fail | Document environment-specific limitations. |
| Users over-trust estimates | Incorrect operational decisions | Label estimates as ranges, expose assumptions, and encourage calibration and staging. |
| Project name may conflict | Discoverability or naming issue | Check before wider publication; rename if necessary. |

## 20. References to verify during implementation

- MySQL 8.0 Reference Manual: InnoDB Online DDL Operations
- MySQL 8.0 Reference Manual: Online DDL algorithms and locks
- MySQL 8.0 Reference Manual: performance_schema.metadata_locks
- Vitess documentation for schemadiff and sqlparser
- gh-ost and pt-online-schema-change prerequisites and limitations
- MySQL 8.0.29 release notes for instant ADD / DROP COLUMN changes

## Appendix A. Example Markdown report

~~~markdown
# mig-est report

- Database: appdb (MySQL 8.0.36; staging database)
- Overall risk: HIGH
- Checked at: 2026-10-30T21:00:00+09:00

## Statements

### [1] ALTER TABLE orders ADD COLUMN status VARCHAR(20)

- Prediction: INPLACE (rule: ADD-COL-02)
- Risk: MEDIUM
- Estimate: 1-4 minutes (low confidence)
- Suggestion: Add nullable, backfill in batches, then apply NOT NULL.

### [2] CREATE INDEX idx_orders_status ON orders (status)

- Prediction: INPLACE; LOCK=NONE may be supported
- Risk: MEDIUM
- Estimate: 2-6 minutes (low confidence)
- Suggestion: Specify ALGORITHM=INPLACE, LOCK=NONE when supported.

## Blockers

- [CRITICAL] MDL-01: A transaction running for 42 minutes touches orders.
- [INFO] REP-01: Replica lag is 2 seconds, within threshold.

## Unknowns

- Disk capacity was not checked because --disk-free was not supplied.
~~~

## Appendix B. Read-only account example

~~~sql
CREATE USER 'migread'@'%' IDENTIFIED BY 'use-a-strong-password';
GRANT SELECT ON appdb.* TO 'migread'@'%';
GRANT SELECT ON performance_schema.* TO 'migread'@'%';
~~~

## Appendix C. Migration examples

~~~sql
-- Risky: leaves the algorithm implicit.
ALTER TABLE orders MODIFY COLUMN status VARCHAR(30);

-- Prefer an explicit algorithm and lock when supported.
ALTER TABLE orders MODIFY COLUMN status VARCHAR(30),
  ALGORITHM=INPLACE, LOCK=NONE;

-- Risky: a heavy operation can dominate a combined ALTER.
ALTER TABLE orders ADD COLUMN note TEXT, ADD PRIMARY KEY (id);

-- Separate lightweight and expensive operations.
ALTER TABLE orders ADD COLUMN note TEXT, ALGORITHM=INSTANT;
-- Review the primary-key change separately and schedule it appropriately.
~~~
