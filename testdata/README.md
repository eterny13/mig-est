# Test fixtures

`migrations/` contains small, reviewable SQL fixtures that exercise the
classifier. `expected/classification.json` is the golden result for MySQL
8.0.28 with no table metadata (the offline, conservative path).

The golden test lives in
`internal/classifier/fixtures_test.go`. Update both the SQL and JSON together
when a classification rule changes intentionally.

These files are never executed by mig-est. They are parsed as text only.
