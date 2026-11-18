# Test fixtures

`migrations/` contains 28 reviewable SQL fixtures (including 26 representative
MySQL 8.0 `ALTER TABLE` and index DDL patterns, plus `CREATE TABLE` and
unsupported trigger coverage) that exercise the parser and classifier.
`expected/classification.json` is the golden result for MySQL 8.0.28 with no
table metadata (the offline, conservative path).

The golden test lives in `internal/classifier/fixtures_test.go` and verifies
both the expected classifications and that every `.sql` file in `migrations/`
is listed in `expected/classification.json`. Update both the SQL and JSON
together when a classification rule changes intentionally.

These files are never executed by mig-est. They are parsed as text only.
