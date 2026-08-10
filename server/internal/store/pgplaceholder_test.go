package store

import "testing"

func TestConvertPlaceholders(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain positional",
			in:   "INSERT INTO t (a, b) VALUES (?, ?)",
			want: "INSERT INTO t (a, b) VALUES ($1, $2)",
		},
		{
			name: "skip inside string literal",
			in:   "WHERE name = '?' AND id = ?",
			want: "WHERE name = '?' AND id = $1",
		},
		{
			name: "no placeholders",
			in:   "SELECT * FROM t",
			want: "SELECT * FROM t",
		},
		{
			// sqlc.arg(name) -> ?N -- must preserve N, not append the running
			// counter. Pre-fix this produced $11 (counter=1, then literal 1).
			name: "sqlc numbered single",
			in:   "VALUES (?1)",
			want: "VALUES ($1)",
		},
		{
			// The bug that motivated the converter fix: CreateWorkspace emits
			// 7 plain ? followed by ?8. Pre-fix produced $1..$7, $88.
			name: "mixed positional then numbered",
			in:   "VALUES (?, ?, ?, ?, ?, ?, ?, ?8)",
			want: "VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
		},
		{
			// workspace_tasks.UpdateWorkspaceTaskByAgent shape: ?1, ?2, ?3
			// repeated, then ?4, ?5.
			name: "numbered multi reuse",
			in:   "SET a = CAST(?1 AS TEXT), b = CAST(?2 AS TEXT) WHERE id = ?3 AND k = ?4",
			want: "SET a = CAST($1 AS TEXT), b = CAST($2 AS TEXT) WHERE id = $3 AND k = $4",
		},
		{
			// Two-digit index survives intact.
			name: "two digit numbered",
			in:   "VALUES (?10)",
			want: "VALUES ($10)",
		},
		{
			// Quoted SQL literals containing ?N must NOT be rewritten.
			name: "numbered inside string literal",
			in:   "WHERE note = '?3' AND id = ?1",
			want: "WHERE note = '?3' AND id = $1",
		},
		{
			// Edge: ? at end of input (no look-ahead byte to read).
			// Must not panic and must emit $<counter>.
			name: "trailing question mark at EOF",
			in:   "SELECT ?",
			want: "SELECT $1",
		},
		{
			// Edge: ? followed by a non-digit alpha char. The trailing alpha
			// is just a normal char and stays in place; the ? becomes $1.
			name: "question mark followed by alpha",
			in:   "WHERE x = ?abc",
			want: "WHERE x = $1abc",
		},
		{
			// Edge: ?0 — sqlc never emits this (param indices start at 1),
			// but the converter must not crash; preserve the digit so the
			// invalid SQL surfaces at PG parse time, not at conversion.
			name: "zero index passes through",
			in:   "VALUES (?0)",
			want: "VALUES ($0)",
		},
		{
			// Edge: ? followed by ? (no digits between). Each ? gets its
			// own counter slot.
			name: "consecutive question marks",
			in:   "VALUES (?, ?)",
			want: "VALUES ($1, $2)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConvertPlaceholders(tc.in)
			if got != tc.want {
				t.Fatalf("ConvertPlaceholders(%q):\n got: %s\nwant: %s", tc.in, got, tc.want)
			}
		})
	}
}
