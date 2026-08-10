package dataconn

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "github.com/trinodb/trino-go-client/trino"
)

// NewTrinoConnector returns a Connector for Trino using trino-go-client
// (database/sql driver name "trino"). Trino only — PrestoDB's protocol
// diverged after the 2019 split (X-Presto-* vs X-Trino-* headers), so this
// driver cannot reach PrestoDB clusters (#362 tracks PrestoDB separately).
//
// readOnlyTx is false because the driver does not support
// sql.TxOptions{ReadOnly: true} (same precedent as ClickHouse/MSSQL); write
// statements are blocked by the service gate before Execute is reached.
//
// DSN: http(s)://user@host:port?catalog=...&schema=... — the Trino catalog
// comes from ConnConfig.Database so the scope "databases" allowlist gates the
// catalog dimension of federated three-part names (catalog.schema.table); the
// default schema comes from Options["schema"], HTTPS from Options["ssl"].
// NOTE: the DSN catalog is only a default — queries can address ANY catalog
// the Trino service user can reach via three-part names, so an empty scope
// "databases" list on a Trino source opens every federated catalog (W1 PoC
// conclusions §4-3: the databases allowlist is the only cross-catalog
// defense). Configure it on any coordinator with more than one catalog.
// The driver only sends basic-auth credentials over https, so a password on a
// plain-http source is ignored by the driver rather than sent in clear.
func NewTrinoConnector() *SQLConnector {
	return &SQLConnector{
		kind:       KindTrino,
		readOnlyTx: false,
		open: func(dsn string) (*sql.DB, error) {
			return sql.Open("trino", dsn)
		},
		dsn: func(c ConnConfig) string {
			port := c.Port
			if port == 0 {
				port = 8080
			}
			scheme := "http"
			if optBool(c.Options, "ssl") {
				scheme = "https"
			}
			user := url.User(c.User)
			if c.Password != "" {
				user = url.UserPassword(c.User, c.Password)
			}
			q := url.Values{}
			if c.Database != "" {
				q.Set("catalog", c.Database)
			}
			if schema := optString(c.Options, "schema"); schema != "" {
				q.Set("schema", schema)
			}
			u := &url.URL{
				Scheme:   scheme,
				User:     user,
				Host:     fmt.Sprintf("%s:%d", c.Host, port),
				RawQuery: q.Encode(),
			}
			return u.String()
		},
	}
}

// optString returns Options[key] as a string ("" when absent or non-string).
func optString(opts map[string]any, key string) string {
	if v, ok := opts[key].(string); ok {
		return v
	}
	return ""
}

// optBool returns Options[key] as a bool. Options come from a decrypted JSON
// config map, so the value may arrive as a bool or a "true"/"1" string.
func optBool(opts map[string]any, key string) bool {
	switch v := opts[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}
