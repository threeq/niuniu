package dataconn

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoURI(t *testing.T) {
	cases := []struct {
		name string
		cc   ConnConfig
		want string
	}{
		{"defaults", ConnConfig{Host: "localhost"}, "mongodb://localhost:27017/"},
		{"explicit port and db", ConnConfig{Host: "db.example.com", Port: 27018, Database: "appdb"},
			"mongodb://db.example.com:27018/appdb"},
		// Credentials are URL-escaped so @ / # ? in passwords cannot break the URI.
		{"credentials escaped", ConnConfig{Host: "h", User: "u@x", Password: "p@/#?", Database: "appdb"},
			"mongodb://u%40x:p%40%2F%23%3F@h:27017/appdb"},
		// String options become query params (e.g. authSource, replicaSet).
		{"options as query params", ConnConfig{Host: "h", User: "root", Password: "s", Database: "appdb",
			Options: map[string]any{"authSource": "admin", "ignored": 42}},
			"mongodb://root:s@h:27017/appdb?authSource=admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mongoURI(tc.cc); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestMongoDocsResultSet(t *testing.T) {
	oid := bson.NewObjectID()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	docs := []bson.D{
		{
			{Key: "_id", Value: oid},
			{Key: "name", Value: "alpha"},
			{Key: "n", Value: int32(7)},
			{Key: "ok", Value: true},
			{Key: "at", Value: bson.NewDateTimeFromTime(ts)},
			{Key: "meta", Value: bson.D{{Key: "tag", Value: "x"}}},
		},
		{
			{Key: "_id", Value: bson.NewObjectID()},
			{Key: "name", Value: "beta"},
			{Key: "extra", Value: bson.A{int64(1), "two"}},
		},
	}

	rs, err := mongoDocsResultSet(docs, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Columns: first-seen order across docs; types refined from values.
	wantCols := []Column{
		{Name: "_id", Type: "string"},
		{Name: "name", Type: "string"},
		{Name: "n", Type: "number"},
		{Name: "ok", Type: "bool"},
		{Name: "at", Type: "time"},
		{Name: "meta", Type: "json"},
		{Name: "extra", Type: "json"},
	}
	if len(rs.Columns) != len(wantCols) {
		t.Fatalf("columns: got %v want %v", rs.Columns, wantCols)
	}
	for i, c := range wantCols {
		if rs.Columns[i] != c {
			t.Fatalf("column %d: got %+v want %+v", i, rs.Columns[i], c)
		}
	}

	if len(rs.Rows) != 2 {
		t.Fatalf("rows: got %d want 2", len(rs.Rows))
	}
	// ObjectID renders as its hex string; DateTime as RFC3339.
	if rs.Rows[0][0] != oid.Hex() {
		t.Fatalf("_id cell: got %v want %s", rs.Rows[0][0], oid.Hex())
	}
	if rs.Rows[0][4] != ts.Format(time.RFC3339) {
		t.Fatalf("time cell: got %v", rs.Rows[0][4])
	}
	// Nested document becomes a plain map (JSON-friendly), not bson.D.
	if m, ok := rs.Rows[0][5].(map[string]any); !ok || m["tag"] != "x" {
		t.Fatalf("nested doc cell: got %#v", rs.Rows[0][5])
	}
	// Keys absent from a doc yield nil cells.
	if rs.Rows[1][2] != nil || rs.Rows[0][6] != nil {
		t.Fatalf("missing-key cells must be nil, got %v / %v", rs.Rows[1][2], rs.Rows[0][6])
	}

	if !rs.Truncated {
		t.Fatal("Truncated flag must pass through")
	}
	if rs.Engine != "mongo" {
		t.Fatalf("engine: got %q", rs.Engine)
	}

	// Raw carries the normalized documents as a JSON array.
	var raw []map[string]any
	if err := json.Unmarshal(rs.Raw, &raw); err != nil {
		t.Fatalf("Raw is not a JSON array: %v", err)
	}
	if len(raw) != 2 || raw[0]["name"] != "alpha" {
		t.Fatalf("Raw content: %s", rs.Raw)
	}
}

func TestMongoDocsResultSetEmpty(t *testing.T) {
	rs, err := mongoDocsResultSet(nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rs.Columns) != 0 || len(rs.Rows) != 0 || rs.Truncated {
		t.Fatalf("empty result: %+v", rs)
	}
	if !strings.HasPrefix(string(rs.Raw), "[") {
		t.Fatalf("Raw must still be a JSON array, got %s", rs.Raw)
	}
}
