package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Client is a read-only catalog connection. It only issues SELECT and SHOW
// statements.
type Client struct {
	db       *sql.DB
	timeout  time.Duration
	database string
	host     string
}

// Open connects to MySQL and verifies connectivity. The DSN is never included
// in returned errors.
func Open(ctx context.Context, dsn string, timeout time.Duration) (*Client, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid MySQL DSN: %w", err)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open MySQL connection: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	c := &Client{db: db, timeout: timeout, database: cfg.DBName, host: cfg.Addr}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot connect to MySQL at %s: %w", cfg.Addr, err)
	}
	return c, nil
}

// Close closes the underlying connection pool.
func (c *Client) Close() error { return c.db.Close() }

// Database returns the database selected in the DSN.
func (c *Client) Database() string { return c.database }

// HostMasked returns host:port without credentials.
func (c *Client) HostMasked() string { return c.host }

// Version returns SELECT VERSION().
func (c *Client) Version(ctx context.Context) (string, error) {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	var v string
	if err := c.db.QueryRowContext(qctx, "SELECT VERSION()").Scan(&v); err != nil {
		return "", fmt.Errorf("cannot read server version: %w", err)
	}
	return v, nil
}

// MaskDSN removes the password from a DSN for safe display. It never returns
// the raw DSN.
func MaskDSN(dsn string) string {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "<invalid DSN>"
	}
	host := cfg.Addr
	if host == "" {
		host = "<default>"
	}
	if cfg.User == "" {
		return fmt.Sprintf("tcp(%s)/%s", host, cfg.DBName)
	}
	return fmt.Sprintf("%s@tcp(%s)/%s", cfg.User, host, cfg.DBName)
}

func (c *Client) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.timeout)
}

func (c *Client) tableSchema(schema string) string {
	if schema != "" {
		return schema
	}
	return c.database
}

// Key returns a normalized lookup key for a schema-qualified table.
func Key(schema, name string) string {
	return strings.ToLower(strings.TrimSpace(schema) + "." + strings.TrimSpace(name))
}

// Lookup returns table metadata by schema and name, falling back to a
// name-only key for offline snapshots.
func (s *Snapshot) Lookup(schema, name string) *TableInfo {
	if s == nil {
		return nil
	}
	schema = strings.TrimSpace(schema)
	name = strings.TrimSpace(name)
	if schema != "" {
		if t := s.Tables[Key(schema, name)]; t != nil {
			return t
		}
	}
	if t := s.Tables[strings.ToLower(name)]; t != nil {
		return t
	}
	if s.Database != "" {
		if t := s.Tables[Key(s.Database, name)]; t != nil {
			return t
		}
	}
	for _, t := range s.Tables {
		if strings.EqualFold(t.Name, name) {
			return t
		}
	}
	return nil
}
