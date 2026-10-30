# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Added

- Initial Go CLI scaffold and offline/online diagnostic prototype.
- Support for `ALTER TABLE`, `CREATE INDEX`, `DROP INDEX`, `CREATE TABLE`,
  `DROP TABLE`, and `RENAME TABLE` diagnostics.
- MySQL 8.0/8.4 algorithm prediction and version-gated INSTANT rules.
- Read-only catalog collection for table sizes, indexes, foreign keys,
  triggers, partitions, MDL blockers, idle transactions, and replication state.
- Time-range and disk estimates with local JSON Lines calibration history.
- Text, Markdown, and JSON reports with `--fail-on` exit codes.
- Expand/contract, explicit ALGORITHM/LOCK, orphan-row, and gh-ost caveats in
  suggestions.
- Docker MySQL 8.0/8.4 development environment and offline golden fixtures.

### Known gaps

- Prebuilt release binaries, Homebrew packaging, and 1M/10M-row benchmark
  results are not published.
- Partition/tablespace handling and the type-pair matrix remain conservative
  and incomplete.
