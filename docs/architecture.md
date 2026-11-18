# mig-est Directory Structure and Architecture Guide

`mig-est` is a read-only diagnostic CLI for MySQL 8.0 and 8.4 (InnoDB) schema migrations. Before DDL statements such as `ALTER TABLE` are applied in production, it predicts the online-DDL algorithm (`INSTANT`, `INPLACE`, `COPY`, or `UNKNOWN`), inspects metadata locks and blockers, estimates duration and disk requirements, and recommends safer alternatives.

---

## Overall Architecture and Execution Pipeline

A diagnostic run flows through the following pipeline:

```text
[SQL files] ───────> parser ───> classifier ───> checker ───> estimator ───> suggester ───> report
                       │              │            │             │
[DB / CLI flags] ──────┴──────────────┴────> catalog             │
                                                                 │
[Measured timings] ────────────────────────────────────────> history
```

---

## Primary Directories

### 1. `cmd/`
Follows the standard Go project layout for executable entry points.

- **`cmd/mig-est/main.go`**
  - A minimal wrapper containing only `main()`.
  - Passes command-line arguments and standard streams to `internal/cli.Run()` and exits with the returned status code via `os.Exit()`.
  - Keeping all application logic under `internal/` allows end-to-end CLI testing without spawning subprocesses.

---

### 2. `internal/`
Contains the core packages of `mig-est`, enforced as private to this module by Go's `internal` package visibility rules. Each package has a single, well-defined responsibility.

| Package | Responsibility |
| :--- | :--- |
| **`internal/cli`** | **Command dispatch and pipeline orchestration**<br>- Routes subcommands (`check`, `record`, `version`, `help`).<br>- Parses and validates CLI flags and positional SQL file arguments (`--file`, `--dir`, `--stdin`, `--offline`, `--table-size`, `--fail-on`, etc.).<br>- Coordinates `parser`, `catalog`, `classifier`, `checker`, `estimator`, `suggester`, and `report`. |
| **`internal/parser`** | **SQL splitting and AST extraction**<br>- Splits multi-statement files while respecting quotes, comments, and `DELIMITER` blocks.<br>- Uses the Vitess SQL parser (`vitess.io/vitess/go/vt/sqlparser`) to extract DDL operations (`ALTER TABLE`, `CREATE/DROP INDEX`, `CREATE/DROP TABLE`, `RENAME TABLE`).<br>- Flags unsupported constructs (triggers, stored programs, events, `DELIMITER` blocks) early rather than treating them as safe. |
| **`internal/classifier`** | **Online-DDL algorithm prediction**<br>- Predicts the MySQL execution algorithm (`INSTANT`, `INPLACE`, `COPY`, or `UNKNOWN`), lock level, and base risk from SQL operations, table metadata, and server version.<br>- Applies MySQL 8.0/8.4 version gates (such as positioned `ADD COLUMN` and `DROP COLUMN` before and after 8.0.29) and type-compatibility rules (integers, strings, decimals, `TEXT`/`BLOB`, `ENUM`/`SET`).<br>- Combines multi-operation `ALTER TABLE` statements conservatively so the most expensive operation governs the statement. |
| **`internal/catalog`** | **Read-only MySQL metadata collection (online mode)**<br>- Issues only `SELECT` and `SHOW` queries with per-query timeouts; never executes DDL or DML.<br>- Collects table size, approximate row counts, engine, `ROW_FORMAT`, columns, indexes, foreign keys, triggers, and partition state from `information_schema`.<br>- Collects active metadata-lock blockers, long-running transactions, idle transactions, and replication lag from `performance_schema`, `INNODB_TRX`, `PROCESSLIST`, and `SHOW REPLICA STATUS`. |
| **`internal/checker`** | **Blocker and context-sensitive risk evaluation**<br>- Evaluates table metadata and runtime snapshots to produce prioritized findings.<br>- Detects missing primary keys, incoming/outgoing foreign keys, triggers, unsupported storage engines, insufficient or unknown disk space, large-table rebuilds, MDL waits, long/idle transactions, and replication issues. |
| **`internal/estimator`** | **Duration-range and disk-space estimation**<br>- Computes conservative time ranges (for example, `55s to 2m 44s`) and confidence levels (`low`, `medium`, `high`) from table byte sizes and algorithm throughput models rather than claiming exact single-point durations.<br>- Calibrates throughput automatically from the median of matching local history records once at least three measurements exist. |
| **`internal/suggester`** | **Actionable safer alternatives**<br>- Recommends expand/contract workflows and checks `gh-ost` / `pt-online-schema-change` prerequisites for `COPY` operations.<br>- Suggests explicit `ALGORITHM=INSTANT, LOCK=NONE` clauses to prevent silent fallback, generates orphan-row check SQL for `ADD FOREIGN KEY`, and recommends splitting multi-operation `ALTER` statements. |
| **`internal/report`** | **Report rendering**<br>- Formats diagnostic results as terminal text (with optional ANSI color and `--quiet` mode), pull-request-friendly Markdown, or machine-readable JSON (intentionally omitting raw SQL and credentials). |
| **`internal/history`** | **Calibration history storage**<br>- Appends and loads real migration timings in JSON Lines format (`.mig-est/history.jsonl`) for `mig-est record`.<br>- Deliberately omits raw SQL text, usernames, passwords, and DSNs. |
| **`internal/model`** | **Shared domain types**<br>- Defines data structures shared across packages (`Statement`, `Operation`, `Finding`, `Estimate`, `Risk`, `Algorithm`, `Severity`, `Report`, etc.). |
| **`internal/config`** | **Configuration loading**<br>- Loads `.mig-est.yaml`, applies documented defaults, and validates thresholds, output format, and `--fail-on` settings. |
| **`internal/mysqlver`** | **MySQL version parsing and feature gates**<br>- Parses MySQL 8.0.x and 8.4.x version strings, rejects unsupported families (such as MariaDB or MySQL 5.7), and evaluates version-dependent feature gates. |
| **`internal/version`** | **Build-time version metadata**<br>- Holds the CLI version, Git commit hash, and build timestamp injected via `-ldflags`. |

---

### 3. `testdata/`
Contains SQL fixtures and golden expectations used by parser and classifier regression tests.

- **`testdata/migrations/`**
  - 28 reviewable SQL fixture files covering 26 representative MySQL 8.0 `ALTER TABLE` and index DDL patterns, plus `CREATE TABLE` and unsupported trigger definitions.
  - Covers `INSTANT` column additions, renames, default changes, and invisible index toggles; `INPLACE` index builds/drops, foreign keys, `CHECK` constraints, and pre-8.0.29 positioned/dropped columns; `COPY` character-set conversions, primary-key changes, engine/row-format rebuilds, and `FORCE`; invalid explicit `ALGORITHM=INSTANT` combinations (`CRITICAL`); and conservative `UNKNOWN` partition operations.
- **`testdata/expected/classification.json`**
  - Golden classification output (predicted algorithm, risk level, and rule IDs) for MySQL 8.0.28 in offline mode without table metadata.
  - Verified by `internal/classifier/fixtures_test.go`, which also ensures every `.sql` fixture in `testdata/migrations/` has a corresponding golden entry.
- **`testdata/README.md`**
  - Guidelines for maintaining and updating the golden fixtures.

---

## Other Directories

- **`docs/`**
  - **`docs/spec.md`**: Full v0.1 specification covering CLI behavior, classification rules, blocker checks, estimation formulas, and exploratory benchmark records.
  - **`docs/architecture.md`**: This directory and package architecture guide.
- **`docker/`**
  - `docker-compose.yml` and `init/01-schema.sql` for spinning up local MySQL 8.0 and 8.4 instances with sample tables and a least-privilege read-only `migread` user for integration testing.
- **`bin/` and `dist/`**
  - Output directories (git-ignored) for local builds (`make build` -> `bin/mig-est`) and cross-platform static release artifacts (`make dist` -> `dist/mig-est-*` and `dist/SHA256SUMS`).
- **`.github/workflows/`**
  - **`ci.yml`**: Runs `gofmt`, `go vet`, `go test -race ./...`, and an offline CLI smoke test on pushes to `main` and pull requests.
  - **`release.yml`**: Validates formatting/vet/tests, cross-compiles static `linux`/`darwin` (`amd64`/`arm64`) binaries with `SHA256SUMS`, smoke-tests the Linux binary, and publishes a GitHub Release with auto-generated release notes on tag pushes or manual dispatch.
