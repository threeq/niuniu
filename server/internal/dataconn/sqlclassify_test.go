package dataconn

import "testing"

func TestClassifySQL(t *testing.T) {
	cases := []struct {
		name    string
		stmt    string
		mode    AccessMode
		objs    []string
		wantErr bool
	}{
		{"select", "SELECT a,b FROM orders WHERE id=1", ModeRead, []string{"orders"}, false},
		{"select join", "SELECT * FROM orders o JOIN users u ON o.uid=u.id", ModeRead, []string{"orders", "users"}, false},
		{"with cte", "WITH t AS (SELECT 1) SELECT * FROM analytics.events", ModeRead, []string{"analytics.events"}, false},
		{"show", "SHOW TABLES", ModeRead, nil, false},
		{"insert", "INSERT INTO orders(a) VALUES(1)", ModeWrite, []string{"orders"}, false},
		{"update", "UPDATE users SET x=1 WHERE id=2", ModeWrite, []string{"users"}, false},
		{"delete", "DELETE FROM users WHERE id=2", ModeWrite, []string{"users"}, false},
		{"ddl drop", "DROP TABLE users", ModeWrite, []string{"users"}, false},
		{"stacked", "SELECT 1; DROP TABLE users", "", nil, true},
		{"empty", "   ", "", nil, true},
		{"comment escape", "SELECT 1 -- ; DROP\nFROM orders", ModeRead, []string{"orders"}, false},
		// C1: CTE/WITH-led writes must classify WRITE (write keyword anywhere).
		{"with cte delete", "WITH x AS (SELECT 1) DELETE FROM users", ModeWrite, []string{"users"}, false},
		{"with cte update", "WITH x AS (SELECT 1) UPDATE orders SET a=1", ModeWrite, []string{"orders"}, false},
		{"with cte insert", "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", ModeWrite, []string{"t"}, false},
		// C2: quoted-identifier extraction (backtick / double-quote / bracket).
		{"backtick qualified", "SELECT * FROM `db`.`tbl`", ModeRead, []string{"db.tbl"}, false},
		{"double-quote qualified", `SELECT * FROM "schema"."events"`, ModeRead, []string{"schema.events"}, false},
		{"bracket qualified", "SELECT * FROM [db].[tbl]", ModeRead, []string{"db.tbl"}, false},
		// C2: CTE alias excluded from objects; base table inside body kept.
		{"cte alias excluded", "WITH t AS (SELECT * FROM analytics.events) SELECT * FROM t", ModeRead, []string{"analytics.events"}, false},
		// ClickHouse dialect
		{"clickhouse show databases", "SHOW DATABASES", ModeRead, nil, false},
		{"clickhouse show tables", "SHOW TABLES FROM mydb", ModeRead, []string{"mydb"}, false},
		{"clickhouse describe", "DESCRIBE TABLE events", ModeRead, []string{"events"}, false},
		{"clickhouse insert", "INSERT INTO events VALUES (1, 'a')", ModeWrite, []string{"events"}, false},
		// SYSTEM command is unrecognized — agents should not run admin commands.
		{"clickhouse system reload", "SYSTEM RELOAD CONFIG", "", nil, true},
		// T-SQL (SQL Server) dialect
		{"tsql select", "SELECT TOP 10 * FROM dbo.orders", ModeRead, []string{"dbo.orders"}, false},
		{"tsql update", "UPDATE dbo.users SET active=1 WHERE id=5", ModeWrite, []string{"dbo.users"}, false},
		// EXEC is not in readLeadRe or writeAnyRe — unrecognized, correctly rejected.
		{"tsql exec sp", "EXEC sp_helptext 'orders'", "", nil, true},
		// Trino federated three-part names must be captured whole — truncating
		// to two segments would make the scope gate check the wrong table.
		{"trino three-part", "SELECT name FROM tpch.tiny.nation LIMIT 3", ModeRead, []string{"tpch.tiny.nation"}, false},
		{"trino three-part join", "SELECT * FROM hive.sales.orders o JOIN tpch.tiny.nation n ON o.nk=n.nationkey", ModeRead, []string{"hive.sales.orders", "tpch.tiny.nation"}, false},
		{"trino quoted three-part", "SELECT * FROM \"hive\".\"sales\".\"orders\"", ModeRead, []string{"hive.sales.orders"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, ref, err := classifySQL(c.stmt)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got mode=%s", mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != c.mode {
				t.Fatalf("mode: got %s want %s", mode, c.mode)
			}
			if !equalStringSet(ref.Objects, c.objs) {
				t.Fatalf("objects: got %v want %v", ref.Objects, c.objs)
			}
		})
	}
}

// TestClassifyReferencesTables verifies the ReferencesTables signal the scope
// gate uses to fail closed: table-referencing statements report true even when
// no object could be parsed; genuinely table-less reads report false.
func TestClassifyReferencesTables(t *testing.T) {
	cases := []struct {
		stmt string
		want bool
	}{
		{"SELECT 1", false},
		{"SHOW TABLES", false},
		{"SELECT id FROM orders", true},
		{"SELECT * FROM `secret`", true},
		// A FROM with an unparseable object still flags table references.
		{"SELECT * FROM (VALUES (1))", true},
	}
	for _, c := range cases {
		_, ref, err := classifySQL(c.stmt)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", c.stmt, err)
		}
		if ref.ReferencesTables != c.want {
			t.Fatalf("%q: ReferencesTables=%v want %v", c.stmt, ref.ReferencesTables, c.want)
		}
	}
}
