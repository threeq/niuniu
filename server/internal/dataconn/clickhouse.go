package dataconn

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "github.com/ClickHouse/clickhouse-go/v2"
)

// NewClickHouseConnector returns a Connector for ClickHouse using the
// clickhouse-go/v2 database/sql interface (driver name "clickhouse").
// readOnlyTx is false because ClickHouse's transaction support is limited;
// the service gate blocks write statements before Execute is reached.
func NewClickHouseConnector() *SQLConnector {
	return &SQLConnector{
		kind:       KindClickHouse,
		readOnlyTx: false,
		open: func(dsn string) (*sql.DB, error) {
			return sql.Open("clickhouse", dsn)
		},
		dsn: func(c ConnConfig) string {
			port := c.Port
			if port == 0 {
				port = 9000
			}
			u := &url.URL{
				Scheme:   "clickhouse",
				User:     url.UserPassword(c.User, c.Password),
				Host:     fmt.Sprintf("%s:%d", c.Host, port),
				Path:     c.Database,
				RawQuery: "dial_timeout=8s&read_timeout=15s",
			}
			return u.String()
		},
	}
}
