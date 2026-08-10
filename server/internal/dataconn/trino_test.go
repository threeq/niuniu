package dataconn

import (
	"testing"
)

func TestTrinoConnectorDSN(t *testing.T) {
	c := NewTrinoConnector()
	if c.Kind() != KindTrino {
		t.Errorf("kind: got %s want %s", c.Kind(), KindTrino)
	}
	// Trino's driver does not support sql.TxOptions{ReadOnly:true}; the service
	// gate is the write barrier (ClickHouse/MSSQL precedent).
	if c.readOnlyTx {
		t.Error("readOnlyTx must be false for trino")
	}

	cases := []struct {
		name string
		conn ConnConfig
		want string
	}{
		{
			"defaults: http, port 8080, catalog from Database, schema from options",
			ConnConfig{User: "u", Host: "h", Database: "hive", Options: map[string]any{"schema": "sales"}},
			"http://u@h:8080?catalog=hive&schema=sales",
		},
		{
			"no catalog/schema: bare DSN, user still set (required by trino)",
			ConnConfig{User: "u", Host: "h"},
			"http://u@h:8080",
		},
		{
			"ssl option switches to https, custom port kept, password included",
			ConnConfig{User: "u", Password: "p@ss/w", Host: "h", Port: 8443, Database: "tpch", Options: map[string]any{"ssl": true}},
			"https://u:p%40ss%2Fw@h:8443?catalog=tpch",
		},
		{
			"ssl as JSON string variant",
			ConnConfig{User: "u", Host: "h", Options: map[string]any{"ssl": "true"}},
			"https://u@h:8080",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.dsn(tc.conn); got != tc.want {
				t.Errorf("dsn: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestTrinoClassifyThreePartName: Trino federated queries address tables as
// catalog.schema.table; Classify must surface the full three-part name so the
// scope gate can check the catalog (databases dimension) and the exact object.
func TestTrinoClassifyThreePartName(t *testing.T) {
	c := NewTrinoConnector()
	mode, ref, err := c.Classify(Operation{
		Statement: "SELECT name FROM tpch.tiny.nation LIMIT 3",
		Database:  "tpch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeRead {
		t.Errorf("mode: got %s want %s", mode, ModeRead)
	}
	if !equalStringSet(ref.Objects, []string{"tpch.tiny.nation"}) {
		t.Errorf("objects: got %v want [tpch.tiny.nation]", ref.Objects)
	}
	if ref.Database != "tpch" {
		t.Errorf("database: got %q want %q", ref.Database, "tpch")
	}

	if mode, _, err := c.Classify(Operation{Statement: "DELETE FROM hive.sales.orders"}); err != nil || mode != ModeWrite {
		t.Errorf("write classify: mode=%s err=%v", mode, err)
	}
}
