package dataconn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestMongoIntegration exercises Ping + the Execute op family against a live
// MongoDB. Skipped unless NIUNIU_TEST_MONGO_HOST is set (port via
// NIUNIU_TEST_MONGO_PORT, default 27017; credentials via
// NIUNIU_TEST_MONGO_USER / NIUNIU_TEST_MONGO_PASSWORD, default none):
//
//	docker run --rm -d -p 27017:27017 mongo:7
//	NIUNIU_TEST_MONGO_HOST=localhost go test ./internal/dataconn/ -run TestMongoIntegration -v
func TestMongoIntegration(t *testing.T) {
	host := os.Getenv("NIUNIU_TEST_MONGO_HOST")
	if host == "" {
		t.Skip("NIUNIU_TEST_MONGO_HOST not set; skipping live MongoDB test")
	}
	port := 27017
	if p := os.Getenv("NIUNIU_TEST_MONGO_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	cc := ConnConfig{
		Kind: KindMongo, Host: host, Port: port,
		User: os.Getenv("NIUNIU_TEST_MONGO_USER"), Password: os.Getenv("NIUNIU_TEST_MONGO_PASSWORD"),
		Database: "niuniu_dataconn_it",
	}
	if cc.User != "" {
		cc.Options = map[string]any{"authSource": "admin"}
	}
	c := NewMongoConnector()
	ctx := context.Background()

	if err := c.Ping(ctx, cc); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	collection := fmt.Sprintf("it_%d", time.Now().UnixNano())
	op := func(mongoOp string) Operation {
		return Operation{Collection: collection, MongoOp: mongoOp, Database: cc.Database, RowLimit: 100, TimeoutMS: 10000}
	}
	exec := func(t *testing.T, o Operation) *ResultSet {
		t.Helper()
		rs, err := c.Execute(ctx, cc, o)
		if err != nil {
			t.Fatalf("%s: %v", o.MongoOp, err)
		}
		return rs
	}
	// Cleanup: deleteMany with empty filter wipes the test collection.
	defer func() { _, _ = c.Execute(ctx, cc, op("deleteMany")) }()

	// insertOne / insertMany
	ins := op("insertOne")
	ins.Document = map[string]any{"name": "alpha", "n": 1, "city": "SH"}
	if rs := exec(t, ins); rs.RowsAffected != 1 {
		t.Fatalf("insertOne RowsAffected=%d", rs.RowsAffected)
	}
	insM := op("insertMany")
	insM.Document = map[string]any{"documents": []any{
		map[string]any{"name": "beta", "n": 2, "city": "SH"},
		map[string]any{"name": "gamma", "n": 3, "city": "BJ"},
	}}
	if rs := exec(t, insM); rs.RowsAffected != 2 {
		t.Fatalf("insertMany RowsAffected=%d", rs.RowsAffected)
	}

	// find with filter
	find := op("find")
	find.Filter = map[string]any{"city": "SH"}
	rs := exec(t, find)
	if len(rs.Rows) != 2 {
		t.Fatalf("find rows=%d want 2 (cols=%v raw=%s)", len(rs.Rows), rs.Columns, rs.Raw)
	}
	var rawDocs []map[string]any
	if err := json.Unmarshal(rs.Raw, &rawDocs); err != nil || len(rawDocs) != 2 {
		t.Fatalf("find Raw: %v %s", err, rs.Raw)
	}

	// countDocuments / estimatedDocumentCount / distinct
	if rs := exec(t, op("countDocuments")); rs.Rows[0][0] != int64(3) {
		t.Fatalf("countDocuments=%v", rs.Rows[0][0])
	}
	exec(t, op("estimatedDocumentCount"))
	dis := op("distinct")
	dis.Document = map[string]any{"field": "city"}
	if rs := exec(t, dis); len(rs.Rows) != 2 {
		t.Fatalf("distinct rows=%d want 2", len(rs.Rows))
	}

	// aggregate ($group)
	agg := op("aggregate")
	agg.Pipeline = []map[string]any{
		{"$group": map[string]any{"_id": "$city", "total": map[string]any{"$sum": "$n"}}},
		{"$sort": map[string]any{"_id": 1}},
	}
	if rs := exec(t, agg); len(rs.Rows) != 2 {
		t.Fatalf("aggregate rows=%d want 2", len(rs.Rows))
	}

	// updateOne / replaceOne / findOneAndDelete / deleteMany
	upd := op("updateOne")
	upd.Filter = map[string]any{"name": "alpha"}
	upd.Document = map[string]any{"$set": map[string]any{"n": 10}}
	if rs := exec(t, upd); rs.RowsAffected != 1 {
		t.Fatalf("updateOne RowsAffected=%d", rs.RowsAffected)
	}
	rep := op("replaceOne")
	rep.Filter = map[string]any{"name": "beta"}
	rep.Document = map[string]any{"name": "beta2", "n": 20, "city": "SZ"}
	if rs := exec(t, rep); rs.RowsAffected != 1 {
		t.Fatalf("replaceOne RowsAffected=%d", rs.RowsAffected)
	}
	fad := op("findOneAndDelete")
	fad.Filter = map[string]any{"name": "gamma"}
	if rs := exec(t, fad); len(rs.Rows) != 1 {
		t.Fatalf("findOneAndDelete rows=%d want 1", len(rs.Rows))
	}
	if rs := exec(t, op("deleteMany")); rs.RowsAffected != 2 {
		t.Fatalf("deleteMany RowsAffected=%d", rs.RowsAffected)
	}

	// RowLimit truncation
	many := op("insertMany")
	docs := make([]any, 5)
	for i := range docs {
		docs[i] = map[string]any{"i": i}
	}
	many.Document = map[string]any{"documents": docs}
	exec(t, many)
	small := op("find")
	small.RowLimit = 3
	if rs := exec(t, small); len(rs.Rows) != 3 || !rs.Truncated {
		t.Fatalf("RowLimit: rows=%d truncated=%v", len(rs.Rows), rs.Truncated)
	}
}
