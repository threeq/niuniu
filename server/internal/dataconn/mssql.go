package dataconn

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "github.com/microsoft/go-mssqldb"
)

// NewMSSQLConnector returns a Connector for Microsoft SQL Server using
// go-mssqldb (driver name "sqlserver"). readOnlyTx is false because
// go-mssqldb does not support sql.TxOptions{ReadOnly: true}; write
// statements are blocked by the service gate before reaching Execute.
func NewMSSQLConnector() *SQLConnector {
	return &SQLConnector{
		kind:       KindMSSQL,
		readOnlyTx: false,
		open: func(dsn string) (*sql.DB, error) {
			return sql.Open("sqlserver", dsn)
		},
		dsn: func(c ConnConfig) string {
			port := c.Port
			if port == 0 {
				port = 1433
			}
			u := &url.URL{
				Scheme: "sqlserver",
				User:   url.UserPassword(c.User, c.Password),
				Host:   fmt.Sprintf("%s:%d", c.Host, port),
				RawQuery: url.Values{
					"database":           {c.Database},
					"connection timeout": {"8"},
				}.Encode(),
			}
			return u.String()
		},
	}
}
