# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Known gaps

- Homebrew tap packaging is planned for v0.2.
- Partition/tablespace handling and uncommon type-conversion pairs remain
  conservative (`UNKNOWN`) and require staging verification.

## [0.1.1] - 2026-11-18

### Added

- Automated GitHub Releases workflow (`.github/workflows/release.yml`) and
  `make dist` target producing static (`CGO_ENABLED=0`) `linux/amd64`,
  `linux/arm64`, `darwin/amd64`, and `darwin/arm64` binaries with `SHA256SUMS`
  and generated release notes.
- Positional SQL file arguments on `mig-est check` and `mig-est record`.
- Expanded offline golden SQL fixture suite in `testdata/migrations` to 28 cases
  (26 representative MySQL 8.0 `ALTER TABLE` and index DDL patterns) with
  orphan-fixture and coverage assertions.
- Online `MDL-01` long-transaction integration test (`TestIntegrationMDLBlocker`)
  and credential non-leakage unit tests.
- English v0.1 specification (`docs/spec.md`), package architecture guide
  (`docs/architecture.md`), and exploratory 1M/10M-row Docker COPY benchmark
  records on MySQL 8.0.46 and 8.4.11.

### Fixed

- `ENUM` to `SET` conversions and `ENUM`/`SET` additions crossing a storage-byte
  boundary are now classified as `COPY` instead of `INSTANT`/`INPLACE`.
- Changing between `BLOB` sizes (`TINYBLOB`, `BLOB`, `MEDIUMBLOB`, `LONGBLOB`)
  now returns `UNKNOWN` (`TYPECHG-03`) consistently with `TEXT` size changes.
- `suggester` now emits `ENUM`/`SET` value-ordering guidance for `SET` columns
  as well as `ENUM` columns.
- `mig-est record` rejects credentials or URL schemes in `--host` so secrets
  cannot be written to `history.jsonl`.
- Trimmed trailing whitespace in `CONVERT TO CHARACTER SET` operation metadata
  and added an explanatory note for `DROP CHECK`.

## [0.1.0] - 2026-10-30

### Added

- Initial Go CLI and offline/online diagnostic engine for MySQL 8.0 and 8.4
  InnoDB schema migrations.
- Support for `ALTER TABLE`, `CREATE INDEX`, `DROP INDEX`, `CREATE TABLE`,
  `DROP TABLE`, and `RENAME TABLE` diagnostics.
- MySQL 8.0/8.4 algorithm prediction and version-gated `INSTANT` rules.
- Read-only catalog collection for table sizes, indexes, foreign keys,
  triggers, partitions, MDL blockers, idle transactions, and replication state.
- Time-range and disk estimates with local JSON Lines calibration history.
- Text, Markdown, and JSON reports with `--fail-on` exit codes.
- Safer alternatives including expand/contract, explicit `ALGORITHM`/`LOCK`,
  foreign-key orphan-row queries, and `gh-ost`/`pt-osc` prerequisite checks.
- Docker MySQL 8.0/8.4 local development environment.
