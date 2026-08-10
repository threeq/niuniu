package dataconn

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestClassifyMongo covers the W1 §2.2 contract: the MongoOp three-state
// whitelist (read / write / denied-or-unknown), aggregate pipeline scanning
// for $out/$merge (write) and $lookup/$graphLookup/$unionWith (extra read
// objects), $facet recursion, and fail-closed rejection of malformed or
// statically-unresolvable stages.
func TestClassifyMongo(t *testing.T) {
	c := NewMongoConnector()
	cases := []struct {
		name     string
		op       Operation
		mode     AccessMode
		objs     []string
		wantErr  string // substring of the expected error; "" = no error
	}{
		// Read whitelist.
		{"find", Operation{Collection: "users", MongoOp: "find", Filter: map[string]any{"age": 30}}, ModeRead, []string{"users"}, ""},
		{"countDocuments", Operation{Collection: "users", MongoOp: "countDocuments"}, ModeRead, []string{"users"}, ""},
		{"estimatedDocumentCount", Operation{Collection: "users", MongoOp: "estimatedDocumentCount"}, ModeRead, []string{"users"}, ""},
		{"distinct", Operation{Collection: "users", MongoOp: "distinct", Document: map[string]any{"field": "city"}}, ModeRead, []string{"users"}, ""},
		{"aggregate plain", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$match": map[string]any{"status": "paid"}},
			{"$group": map[string]any{"_id": "$city", "n": map[string]any{"$sum": 1}}},
		}}, ModeRead, []string{"orders"}, ""},

		// Write whitelist.
		{"insertOne", Operation{Collection: "users", MongoOp: "insertOne", Document: map[string]any{"name": "a"}}, ModeWrite, []string{"users"}, ""},
		{"insertMany", Operation{Collection: "users", MongoOp: "insertMany"}, ModeWrite, []string{"users"}, ""},
		{"updateOne", Operation{Collection: "users", MongoOp: "updateOne"}, ModeWrite, []string{"users"}, ""},
		{"updateMany", Operation{Collection: "users", MongoOp: "updateMany"}, ModeWrite, []string{"users"}, ""},
		{"replaceOne", Operation{Collection: "users", MongoOp: "replaceOne"}, ModeWrite, []string{"users"}, ""},
		{"deleteOne", Operation{Collection: "users", MongoOp: "deleteOne"}, ModeWrite, []string{"users"}, ""},
		{"deleteMany", Operation{Collection: "users", MongoOp: "deleteMany"}, ModeWrite, []string{"users"}, ""},
		{"findOneAndUpdate", Operation{Collection: "users", MongoOp: "findOneAndUpdate"}, ModeWrite, []string{"users"}, ""},
		{"findOneAndReplace", Operation{Collection: "users", MongoOp: "findOneAndReplace"}, ModeWrite, []string{"users"}, ""},
		{"findOneAndDelete", Operation{Collection: "users", MongoOp: "findOneAndDelete"}, ModeWrite, []string{"users"}, ""},
		// bulkWrite is rejected at Classify (no structured encoding for its
		// operation array) instead of classifying as a write that can only
		// fail at Execute after the user confirmed it.
		{"bulkWrite", Operation{Collection: "users", MongoOp: "bulkWrite"}, "", nil, "bulkWrite"},

		// Aggregate write stages: $out / $merge anywhere in the pipeline.
		{"aggregate $out string", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$match": map[string]any{}},
			{"$out": "report"},
		}}, ModeWrite, []string{"orders", "report"}, ""},
		{"aggregate $out db+coll", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$out": map[string]any{"db": "warehouse", "coll": "report"}},
		}}, ModeWrite, []string{"orders", "warehouse.report"}, ""},
		{"aggregate $merge string", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": "rollup"},
		}}, ModeWrite, []string{"orders", "rollup"}, ""},
		{"aggregate $merge into string", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": map[string]any{"into": "rollup", "whenMatched": "replace"}},
		}}, ModeWrite, []string{"orders", "rollup"}, ""},
		{"aggregate $merge into db+coll", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": map[string]any{"into": map[string]any{"db": "warehouse", "coll": "rollup"}}},
		}}, ModeWrite, []string{"orders", "warehouse.rollup"}, ""},

		// Cross-collection read stages add their target to Objects.
		{"aggregate $lookup", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$lookup": map[string]any{"from": "users", "localField": "uid", "foreignField": "_id", "as": "u"}},
		}}, ModeRead, []string{"orders", "users"}, ""},
		{"aggregate $graphLookup", Operation{Collection: "orgs", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$graphLookup": map[string]any{"from": "orgs2", "startWith": "$pid", "connectFromField": "pid", "connectToField": "_id", "as": "tree"}},
		}}, ModeRead, []string{"orgs", "orgs2"}, ""},
		{"aggregate $unionWith string", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$unionWith": "b"},
		}}, ModeRead, []string{"a", "b"}, ""},
		{"aggregate $unionWith coll map", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$unionWith": map[string]any{"coll": "b", "pipeline": []any{map[string]any{"$match": map[string]any{}}}}},
		}}, ModeRead, []string{"a", "b"}, ""},
		{"objects deduped", Operation{Collection: "users", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$lookup": map[string]any{"from": "users"}},
		}}, ModeRead, []string{"users"}, ""},

		// $facet recursion: a write stage hidden in a sub-pipeline still flips write.
		{"facet hides $out", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$facet": map[string]any{
				"a": []any{map[string]any{"$match": map[string]any{}}},
				"b": []any{map[string]any{"$out": "leak"}},
			}},
		}}, ModeWrite, []string{"orders", "leak"}, ""},
		// Nested $lookup.pipeline is scanned too: its $unionWith target joins Objects.
		{"lookup sub-pipeline unionWith", Operation{Collection: "orders", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$lookup": map[string]any{"from": "users", "pipeline": []any{
				map[string]any{"$unionWith": "audit"},
			}}},
		}}, ModeRead, []string{"orders", "users", "audit"}, ""},

		// Denied ops (admin/DDL surface): rejected outright, not classified write.
		{"drop denied", Operation{Collection: "users", MongoOp: "drop"}, "", nil, "denied"},
		{"dropDatabase denied", Operation{Collection: "users", MongoOp: "dropDatabase"}, "", nil, "denied"},
		{"createIndex denied", Operation{Collection: "users", MongoOp: "createIndex"}, "", nil, "denied"},
		{"dropIndex denied", Operation{Collection: "users", MongoOp: "dropIndex"}, "", nil, "denied"},
		{"createCollection denied", Operation{Collection: "users", MongoOp: "createCollection"}, "", nil, "denied"},
		{"renameCollection denied", Operation{Collection: "users", MongoOp: "renameCollection"}, "", nil, "denied"},
		{"runCommand denied", Operation{Collection: "users", MongoOp: "runCommand"}, "", nil, "denied"},

		// Unknown ops rejected (three-state whitelist); op names are case-sensitive.
		{"unknown op", Operation{Collection: "users", MongoOp: "mapReduce"}, "", nil, "not whitelisted"},
		{"case sensitive", Operation{Collection: "users", MongoOp: "FIND"}, "", nil, "not whitelisted"},
		{"empty op", Operation{Collection: "users"}, "", nil, "requires"},
		{"empty collection", Operation{MongoOp: "find"}, "", nil, "requires"},

		// Malformed / statically-unresolvable stages: fail closed.
		{"stage two keys", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$match": map[string]any{}, "$limit": 1},
		}}, "", nil, "exactly one"},
		{"stage no dollar", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"match": map[string]any{}},
		}}, "", nil, "$"},
		{"empty stage", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{{}}}, "", nil, "exactly one"},
		{"$out expression", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$out": 42},
		}}, "", nil, "$out"},
		{"$out map missing coll", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$out": map[string]any{"db": "x"}},
		}}, "", nil, "$out"},
		{"$merge into missing", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": map[string]any{"whenMatched": "replace"}},
		}}, "", nil, "$merge"},
		{"$lookup missing from", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$lookup": map[string]any{"pipeline": []any{}}},
		}}, "", nil, "$lookup"},
		{"$unionWith map missing coll", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$unionWith": map[string]any{"pipeline": []any{}}},
		}}, "", nil, "$unionWith"},
		{"$facet sub not pipeline", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$facet": map[string]any{"x": "notapipeline"}},
		}}, "", nil, "$facet"},
		{"aggregate without pipeline", Operation{Collection: "a", MongoOp: "aggregate"}, "", nil, "pipeline"},

		// Dotted bare collection names are rejected fail-closed: checkScopeMongo
		// splits Objects on the first dot ("archive.users" → db=archive,
		// coll=users), so a dotted name would be scope-checked as the wrong
		// collection while Execute reads the real "archive.users". The dot is
		// reserved for connector-built cross-db targets ($out/$merge {db,coll}).
		{"dotted main collection", Operation{Collection: "archive.users", MongoOp: "find"}, "", nil, "dot"},
		{"dotted lookup from", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$lookup": map[string]any{"from": "archive.users"}},
		}}, "", nil, "dot"},
		{"dotted unionWith string", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$unionWith": "archive.users"},
		}}, "", nil, "dot"},
		{"dotted unionWith coll", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$unionWith": map[string]any{"coll": "archive.users"}},
		}}, "", nil, "dot"},
		{"dotted $out string", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$out": "x.report"},
		}}, "", nil, "dot"},
		{"dotted $out coll segment", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$out": map[string]any{"db": "w", "coll": "r.x"}},
		}}, "", nil, "dot"},
		{"dotted $merge into string", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": map[string]any{"into": "x.report"}},
		}}, "", nil, "dot"},
		{"dotted $merge into coll segment", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": map[string]any{"into": map[string]any{"db": "w", "coll": "r.x"}}},
		}}, "", nil, "dot"},
		// A db segment that is present but not a usable string is rejected
		// (fail-closed), never silently dropped to same-db semantics.
		{"$out non-string db", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$out": map[string]any{"db": 42, "coll": "r"}},
		}}, "", nil, "db"},
		{"$merge non-string db", Operation{Collection: "a", MongoOp: "aggregate", Pipeline: []map[string]any{
			{"$merge": map[string]any{"into": map[string]any{"db": map[string]any{"$expr": 1}, "coll": "r"}}},
		}}, "", nil, "db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, ref, err := c.Classify(tc.op)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got mode=%s objs=%v", tc.wantErr, mode, ref.Objects)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.mode {
				t.Fatalf("mode: got %s want %s", mode, tc.mode)
			}
			if !equalStringSet(ref.Objects, tc.objs) {
				t.Fatalf("objects: got %v want %v", ref.Objects, tc.objs)
			}
			if ref.CommandCls != tc.op.MongoOp {
				t.Fatalf("CommandCls: got %q want %q", ref.CommandCls, tc.op.MongoOp)
			}
			if !ref.ReferencesTables {
				t.Fatal("ReferencesTables must be true for mongo ops")
			}
		})
	}
}

// TestClassifyMongoDatabaseAndDepth: ref.Database carries the injected
// database, and pathological $facet nesting is cut off by the depth cap.
func TestClassifyMongoDatabaseAndDepth(t *testing.T) {
	c := NewMongoConnector()
	mode, ref, err := c.Classify(Operation{Collection: "users", MongoOp: "find", Database: "appdb"})
	if err != nil || mode != ModeRead || ref.Database != "appdb" {
		t.Fatalf("got mode=%s db=%q err=%v", mode, ref.Database, err)
	}

	// Build a $facet chain deeper than the cap.
	stage := map[string]any{"$match": map[string]any{}}
	for i := 0; i < 20; i++ {
		stage = map[string]any{"$facet": map[string]any{"f": []any{stage}}}
	}
	_, _, err = c.Classify(Operation{Collection: "users", MongoOp: "aggregate", Pipeline: []map[string]any{stage}})
	if err == nil || !strings.Contains(err.Error(), "deep") {
		t.Fatalf("want depth error, got %v", err)
	}
}

func TestMongoConnectorKind(t *testing.T) {
	if k := NewMongoConnector().Kind(); k != KindMongo {
		t.Fatalf("Kind: got %s want %s", k, KindMongo)
	}
}

func TestMongoExecuteRevalidates(t *testing.T) {
	// Defense in depth: Execute re-runs Classify before connecting, so a
	// denied op or a write-stage pipeline handed directly to Execute fails
	// fast (ConnConfig is empty — reaching the dial would error differently).
	c := NewMongoConnector()
	for _, tc := range []struct {
		name string
		op   Operation
	}{
		{"denied op", Operation{Collection: "users", MongoOp: "drop"}},
		{"unknown op", Operation{Collection: "users", MongoOp: "mapReduce"}},
		{"bulkWrite", Operation{Collection: "users", MongoOp: "bulkWrite"}},
	} {
		_, err := c.Execute(context.Background(), ConnConfig{}, tc.op)
		if err == nil || !errors.Is(err, ErrDeniedByPolicy) {
			t.Errorf("%s: want ErrDeniedByPolicy from Execute, got %v", tc.name, err)
		}
	}
	// Malformed pipelines are also caught pre-dial (plain error, fail-closed).
	_, err := c.Execute(context.Background(), ConnConfig{}, Operation{
		Collection: "a", MongoOp: "aggregate",
		Pipeline: []map[string]any{{"$lookup": map[string]any{"from": "b", "pipeline": "boom"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "pipeline must be an array") {
		t.Errorf("type-confused sub-pipeline: want fail-closed error, got %v", err)
	}
}
