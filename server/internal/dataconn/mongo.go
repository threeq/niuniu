package dataconn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// MongoConnector implements Connector for MongoDB (Epic #345 Wave3 #356).
// Operations arrive structured (Collection + MongoOp + Filter/Pipeline/
// Document — the W2 gateway op contract); Classify enforces the W1 §2.2
// three-state MongoOp whitelist and scans aggregate pipelines so that
// $out/$merge classify as writes and cross-collection stages ($lookup,
// $graphLookup, $unionWith) surface their targets in ResourceRef.Objects
// for the scope gate.
type MongoConnector struct{}

func NewMongoConnector() *MongoConnector { return &MongoConnector{} }

func (c *MongoConnector) Kind() SourceKind { return KindMongo }

// MongoOp whitelists (W1 §2.2). Ops not listed in either set are rejected
// outright — including the admin/DDL surface (drop, createIndex, runCommand,
// ...), which is denied rather than classified as a confirmable write.
var (
	mongoReadOps = map[string]bool{
		"find":                   true,
		"countDocuments":         true,
		"estimatedDocumentCount": true,
		"distinct":               true,
		// "aggregate" is read unless the pipeline contains $out/$merge —
		// handled separately in Classify.
	}
	mongoWriteOps = map[string]bool{
		"insertOne":         true,
		"insertMany":        true,
		"updateOne":         true,
		"updateMany":        true,
		"replaceOne":        true,
		"deleteOne":         true,
		"deleteMany":        true,
		"findOneAndUpdate":  true,
		"findOneAndReplace": true,
		"findOneAndDelete":  true,
		// "bulkWrite" is rejected in Classify: the structured op contract has
		// no encoding for its operation array, so classifying it as a
		// confirmable write would walk the user through a confirmation that
		// can only end in an Execute error.
	}
	mongoDeniedOps = map[string]bool{
		"drop":             true,
		"dropDatabase":     true,
		"createIndex":      true,
		"createIndexes":    true,
		"dropIndex":        true,
		"dropIndexes":      true,
		"createCollection": true,
		"renameCollection": true,
		"runCommand":       true,
	}
)

// mongoMaxPipelineDepth caps $facet / sub-pipeline recursion during
// classification (mirrors the audit summarizer's depth cap): a deeper
// pipeline is rejected rather than partially scanned.
const mongoMaxPipelineDepth = 10

// mongoBareName validates an agent-supplied bare collection name. Dots are
// rejected fail-closed: checkScopeMongo splits every ResourceRef object on the
// first dot ("archive.users" → db=archive, coll=users), so a dotted bare name
// would be scope-checked as the wrong collection while Execute touches the
// real "archive.users". The dot is reserved for connector-built cross-db
// targets (mongoOutTarget/mongoMergeTarget {db,coll} encoding); collections
// whose own name contains a dot are unreachable through the data proxy.
func mongoBareName(what, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("mongo %s requires a collection name", what)
	}
	if strings.Contains(name, ".") {
		return "", fmt.Errorf("mongo %s %q must not contain a dot (dotted names are reserved for cross-db targets; dotted collections are not reachable through the data proxy)", what, name)
	}
	return name, nil
}

func (c *MongoConnector) Classify(op Operation) (AccessMode, ResourceRef, error) {
	if op.Collection == "" || op.MongoOp == "" {
		return "", ResourceRef{}, fmt.Errorf("mongo operation requires collection and mongo_op")
	}
	if _, err := mongoBareName("collection", op.Collection); err != nil {
		return "", ResourceRef{}, err
	}
	ref := ResourceRef{
		Database:         op.Database,
		Objects:          []string{op.Collection},
		CommandCls:       op.MongoOp,
		ReferencesTables: true,
	}
	switch {
	case mongoReadOps[op.MongoOp]:
		return ModeRead, ref, nil
	case mongoWriteOps[op.MongoOp]:
		return ModeWrite, ref, nil
	case op.MongoOp == "aggregate":
		if len(op.Pipeline) == 0 {
			return "", ResourceRef{}, fmt.Errorf("mongo aggregate requires a non-empty pipeline")
		}
		stages := make([]any, len(op.Pipeline))
		for i, s := range op.Pipeline {
			stages[i] = s
		}
		objs, write, err := scanMongoPipeline(stages, 0)
		if err != nil {
			return "", ResourceRef{}, err
		}
		ref.Objects = dedupStrings(append(ref.Objects, objs...))
		if write {
			return ModeWrite, ref, nil
		}
		return ModeRead, ref, nil
	case mongoDeniedOps[op.MongoOp]:
		return "", ResourceRef{}, fmt.Errorf("%w: mongo op %q is denied (admin/DDL surface is not exposed through the data proxy)", ErrDeniedByPolicy, op.MongoOp)
	case op.MongoOp == "bulkWrite":
		return "", ResourceRef{}, fmt.Errorf("%w: mongo bulkWrite has no structured encoding through the data proxy; use insertMany/updateMany/deleteMany", ErrDeniedByPolicy)
	default:
		return "", ResourceRef{}, fmt.Errorf("%w: mongo op %q is not whitelisted", ErrDeniedByPolicy, op.MongoOp)
	}
}

// scanMongoPipeline walks every stage (W1 §2.2: scan ALL stages, not just the
// last, defense first) collecting cross-collection targets and detecting write
// stages. It fails closed: malformed stages and statically-unresolvable
// collection references (expressions, missing fields) are errors, never
// silently ignored.
func scanMongoPipeline(stages []any, depth int) (objs []string, write bool, err error) {
	if depth > mongoMaxPipelineDepth {
		return nil, false, fmt.Errorf("mongo pipeline nested too deep (max %d levels)", mongoMaxPipelineDepth)
	}
	for _, raw := range stages {
		stage, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("mongo pipeline stage must be a document, got %T", raw)
		}
		if len(stage) != 1 {
			return nil, false, fmt.Errorf("mongo pipeline stage must have exactly one key, got %d", len(stage))
		}
		var name string
		var val any
		for k, v := range stage {
			name, val = k, v
		}
		if !strings.HasPrefix(name, "$") {
			return nil, false, fmt.Errorf("mongo pipeline stage key %q must start with \"$\"", name)
		}
		var subObjs []string
		var subWrite bool
		var subErr error
		switch name {
		case "$out":
			obj, e := mongoOutTarget(val)
			if e != nil {
				return nil, false, e
			}
			objs, write = append(objs, obj), true
		case "$merge":
			obj, e := mongoMergeTarget(val)
			if e != nil {
				return nil, false, e
			}
			objs, write = append(objs, obj), true
		case "$lookup", "$graphLookup":
			m, ok := val.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("mongo %s stage requires a document value", name)
			}
			from, ok := m["from"].(string)
			if !ok {
				return nil, false, fmt.Errorf("mongo %s stage requires a static string \"from\" collection", name)
			}
			obj, e := mongoBareName(name+" from", from)
			if e != nil {
				return nil, false, e
			}
			objs = append(objs, obj)
			subObjs, subWrite, subErr = scanMongoSubPipeline(name, m, depth)
		case "$unionWith":
			switch v := val.(type) {
			case string:
				obj, e := mongoBareName("$unionWith collection", v)
				if e != nil {
					return nil, false, e
				}
				objs = append(objs, obj)
			case map[string]any:
				coll, ok := v["coll"].(string)
				if !ok {
					return nil, false, fmt.Errorf("mongo $unionWith stage requires a static string \"coll\" collection")
				}
				obj, e := mongoBareName("$unionWith coll", coll)
				if e != nil {
					return nil, false, e
				}
				objs = append(objs, obj)
				subObjs, subWrite, subErr = scanMongoSubPipeline(name, v, depth)
			default:
				return nil, false, fmt.Errorf("mongo $unionWith stage requires a collection name or {coll, pipeline} document")
			}
		case "$facet":
			m, ok := val.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("mongo $facet stage requires a document of sub-pipelines")
			}
			for field, rawSub := range m {
				sub, ok := rawSub.([]any)
				if !ok {
					return nil, false, fmt.Errorf("mongo $facet field %q must be a sub-pipeline array", field)
				}
				fObjs, fWrite, fErr := scanMongoPipeline(sub, depth+1)
				if fErr != nil {
					return nil, false, fErr
				}
				subObjs = append(subObjs, fObjs...)
				subWrite = subWrite || fWrite
			}
		}
		if subErr != nil {
			return nil, false, subErr
		}
		objs = append(objs, subObjs...)
		write = write || subWrite
	}
	return objs, write, nil
}

// scanMongoSubPipeline scans the optional "pipeline" field of a $lookup /
// $graphLookup / $unionWith document. A "pipeline" key that is present but not
// an array is rejected fail-closed — a type-confused sub-pipeline must never
// silently skip the write/cross-collection scan.
func scanMongoSubPipeline(stage string, m map[string]any, depth int) ([]string, bool, error) {
	raw, ok := m["pipeline"]
	if !ok {
		return nil, false, nil
	}
	sub, ok := raw.([]any)
	if !ok {
		return nil, false, fmt.Errorf("mongo %s pipeline must be an array, got %T", stage, raw)
	}
	return scanMongoPipeline(sub, depth+1)
}

// mongoOutTarget resolves a $out stage value: "coll" or {db, coll}. A cross-db
// target encodes as "db.coll" (the scope gate splits on the first dot and runs
// the db segment through the databases allow-list); both segments must
// themselves be dot-free so the encoding stays unambiguous.
func mongoOutTarget(val any) (string, error) {
	switch v := val.(type) {
	case string:
		if v != "" {
			return mongoBareName("$out collection", v)
		}
	case map[string]any:
		coll, ok := v["coll"].(string)
		if !ok || coll == "" {
			break
		}
		return mongoQualifiedTarget("$out", v["db"], coll)
	}
	return "", fmt.Errorf("mongo $out stage requires a collection name or {db, coll} document")
}

// mongoMergeTarget resolves a $merge stage value: "coll" or {into: "coll"} or
// {into: {db, coll}}.
func mongoMergeTarget(val any) (string, error) {
	switch v := val.(type) {
	case string:
		if v != "" {
			return mongoBareName("$merge collection", v)
		}
	case map[string]any:
		switch into := v["into"].(type) {
		case string:
			if into != "" {
				return mongoBareName("$merge into", into)
			}
		case map[string]any:
			coll, ok := into["coll"].(string)
			if !ok || coll == "" {
				break
			}
			return mongoQualifiedTarget("$merge into", into["db"], coll)
		}
	}
	return "", fmt.Errorf("mongo $merge stage requires a static \"into\" target (collection name or {db, coll})")
}

// mongoQualifiedTarget builds the "db.coll" cross-db object (or bare coll when
// db is absent), rejecting dots inside either segment so the first-dot split
// in checkScopeMongo cannot be confused. A db key that is present but not a
// non-empty string (expression, number, ...) is rejected fail-closed — never
// silently dropped to same-db semantics.
func mongoQualifiedTarget(what string, dbVal any, coll string) (string, error) {
	c, err := mongoBareName(what+" coll", coll)
	if err != nil {
		return "", err
	}
	if dbVal == nil {
		return c, nil
	}
	db, ok := dbVal.(string)
	if !ok || db == "" {
		return "", fmt.Errorf("mongo %s db must be a non-empty static string", what)
	}
	if strings.Contains(db, ".") {
		return "", fmt.Errorf("mongo %s db %q must not contain a dot", what, db)
	}
	return db + "." + c, nil
}

func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// mongoURI builds the connection string. Credentials go through
// url.UserPassword so special characters (@ / # ?) cannot break the URI.
// The configured database sits in the path, which also makes it the default
// authSource; users created in admin set options {"authSource": "admin"} —
// every string-valued ConnConfig.Options entry becomes a query parameter
// (authSource, replicaSet, tls, ...).
func mongoURI(c ConnConfig) string {
	port := c.Port
	if port == 0 {
		port = 27017
	}
	u := &url.URL{
		Scheme: "mongodb",
		Host:   fmt.Sprintf("%s:%d", c.Host, port),
		Path:   "/" + c.Database,
	}
	if c.User != "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	q := url.Values{}
	for k, v := range c.Options {
		if s, ok := v.(string); ok {
			q.Set(k, s)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// mongoConnect opens a short-lived client (M1 pattern: one client per
// Ping/Execute, no pooling yet — see Pool). Callers must call the returned
// closer.
func mongoConnect(conn ConnConfig) (*mongo.Client, func(), error) {
	client, err := mongo.Connect(options.Client().
		ApplyURI(mongoURI(conn)).
		SetConnectTimeout(8 * time.Second).
		SetServerSelectionTimeout(8 * time.Second))
	if err != nil {
		return nil, nil, err
	}
	closer := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Disconnect(ctx)
	}
	return client, closer, nil
}

func (c *MongoConnector) Ping(ctx context.Context, conn ConnConfig) error {
	client, closer, err := mongoConnect(conn)
	if err != nil {
		return err
	}
	defer closer()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return client.Ping(ctx, readpref.Primary())
}

// Execute dispatches the structured op to the matching driver call and
// normalizes the outcome: document results flatten into Columns/Rows with the
// normalized documents in Raw; counts and write acknowledgements become a
// one-row summary / RowsAffected. The service gate has already classified and
// scope-checked the op; Execute trusts op.Database/RowLimit/TimeoutMS as
// service-injected values.
func (c *MongoConnector) Execute(ctx context.Context, conn ConnConfig, op Operation) (*ResultSet, error) {
	// Defense in depth (mirrors ESConnector.Execute): re-run Classify so a
	// denied op or a pipeline containing $out/$merge can never execute as a
	// "read" even if a future caller bypasses the gateway. The mode also
	// drives the read-only $limit pushdown below.
	mode, _, err := c.Classify(op)
	if err != nil {
		return nil, err
	}

	to := time.Duration(op.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	client, closer, err := mongoConnect(conn)
	if err != nil {
		return nil, err
	}
	defer closer()

	limit := op.RowLimit
	if limit <= 0 {
		limit = 1000
	}
	coll := client.Database(op.Database).Collection(op.Collection)
	filter := op.Filter
	if filter == nil {
		filter = map[string]any{}
	}

	start := time.Now()
	rs, err := executeMongoOp(ctx, coll, op, filter, limit, mode)
	if err != nil {
		return nil, err
	}
	rs.DurationMS = time.Since(start).Milliseconds()
	return rs, nil
}

func executeMongoOp(ctx context.Context, coll *mongo.Collection, op Operation, filter map[string]any, limit int, mode AccessMode) (*ResultSet, error) {
	switch op.MongoOp {
	case "find":
		// limit+1 server-side: one extra doc lets collectMongoCursor detect
		// truncation without the server streaming an unbounded result.
		cur, err := coll.Find(ctx, filter, options.Find().SetLimit(int64(limit)+1))
		if err != nil {
			return nil, err
		}
		return collectMongoCursor(ctx, cur, limit)

	case "countDocuments":
		n, err := coll.CountDocuments(ctx, filter)
		if err != nil {
			return nil, err
		}
		return mongoCountResultSet("count", n)

	case "estimatedDocumentCount":
		n, err := coll.EstimatedDocumentCount(ctx)
		if err != nil {
			return nil, err
		}
		return mongoCountResultSet("count", n)

	case "distinct":
		field, _ := op.Document["field"].(string)
		if field == "" {
			return nil, fmt.Errorf(`mongo distinct requires document {"field": "<field name>"}`)
		}
		var vals bson.A
		if err := coll.Distinct(ctx, field, filter).Decode(&vals); err != nil {
			return nil, err
		}
		truncated := false
		if len(vals) > limit {
			vals, truncated = vals[:limit], true
		}
		docs := make([]bson.D, len(vals))
		for i, v := range vals {
			docs[i] = bson.D{{Key: field, Value: v}}
		}
		return mongoDocsResultSet(docs, truncated)

	case "aggregate":
		stages := make(bson.A, len(op.Pipeline))
		for i, s := range op.Pipeline {
			stages[i] = s
		}
		// Read pipelines get a server-side $limit pushdown (limit+1 keeps
		// truncation detectable). Write pipelines are excluded: $out/$merge
		// must be the final stage, so appending would be a syntax error.
		if mode == ModeRead {
			stages = append(stages, bson.M{"$limit": int64(limit) + 1})
		}
		cur, err := coll.Aggregate(ctx, stages)
		if err != nil {
			return nil, err
		}
		return collectMongoCursor(ctx, cur, limit)

	case "insertOne":
		if op.Document == nil {
			return nil, fmt.Errorf("mongo insertOne requires document")
		}
		res, err := coll.InsertOne(ctx, op.Document)
		if err != nil {
			return nil, err
		}
		return mongoWriteResultSet(1, map[string]any{"inserted_id": normalizeMongoValue(res.InsertedID)})

	case "insertMany":
		raw, _ := op.Document["documents"].([]any)
		if len(raw) == 0 {
			return nil, fmt.Errorf(`mongo insertMany requires document {"documents": [<doc>, ...]}`)
		}
		res, err := coll.InsertMany(ctx, raw)
		if err != nil {
			return nil, err
		}
		ids := make([]any, len(res.InsertedIDs))
		for i, id := range res.InsertedIDs {
			ids[i] = normalizeMongoValue(id)
		}
		return mongoWriteResultSet(int64(len(ids)), map[string]any{"inserted_ids": ids})

	case "updateOne", "updateMany":
		if op.Document == nil {
			return nil, fmt.Errorf("mongo %s requires an update document", op.MongoOp)
		}
		var res *mongo.UpdateResult
		var err error
		if op.MongoOp == "updateOne" {
			res, err = coll.UpdateOne(ctx, filter, op.Document)
		} else {
			res, err = coll.UpdateMany(ctx, filter, op.Document)
		}
		if err != nil {
			return nil, err
		}
		return mongoWriteResultSet(res.ModifiedCount+res.UpsertedCount, map[string]any{
			"matched": res.MatchedCount, "modified": res.ModifiedCount, "upserted": res.UpsertedCount,
		})

	case "replaceOne":
		if op.Document == nil {
			return nil, fmt.Errorf("mongo replaceOne requires a replacement document")
		}
		res, err := coll.ReplaceOne(ctx, filter, op.Document)
		if err != nil {
			return nil, err
		}
		return mongoWriteResultSet(res.ModifiedCount+res.UpsertedCount, map[string]any{
			"matched": res.MatchedCount, "modified": res.ModifiedCount, "upserted": res.UpsertedCount,
		})

	case "deleteOne", "deleteMany":
		var res *mongo.DeleteResult
		var err error
		if op.MongoOp == "deleteOne" {
			res, err = coll.DeleteOne(ctx, filter)
		} else {
			res, err = coll.DeleteMany(ctx, filter)
		}
		if err != nil {
			return nil, err
		}
		return mongoWriteResultSet(res.DeletedCount, map[string]any{"deleted": res.DeletedCount})

	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete":
		var sr *mongo.SingleResult
		switch op.MongoOp {
		case "findOneAndUpdate":
			if op.Document == nil {
				return nil, fmt.Errorf("mongo findOneAndUpdate requires an update document")
			}
			sr = coll.FindOneAndUpdate(ctx, filter, op.Document)
		case "findOneAndReplace":
			if op.Document == nil {
				return nil, fmt.Errorf("mongo findOneAndReplace requires a replacement document")
			}
			sr = coll.FindOneAndReplace(ctx, filter, op.Document)
		default:
			sr = coll.FindOneAndDelete(ctx, filter)
		}
		var doc bson.D
		if err := sr.Decode(&doc); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return mongoDocsResultSet(nil, false)
			}
			return nil, err
		}
		return mongoDocsResultSet([]bson.D{doc}, false)

	default:
		// Unreachable: Execute re-runs Classify, which rejects unknown ops
		// (and bulkWrite) before this dispatch.
		return nil, fmt.Errorf("mongo op %q is not supported", op.MongoOp)
	}
}

// collectMongoCursor drains up to limit documents, flagging truncation when
// more remain. Field order of the first occurrence drives column order.
func collectMongoCursor(ctx context.Context, cur *mongo.Cursor, limit int) (*ResultSet, error) {
	defer cur.Close(ctx)
	var docs []bson.D
	truncated := false
	for cur.Next(ctx) {
		if len(docs) >= limit {
			truncated = true
			break
		}
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	if !truncated {
		if err := cur.Err(); err != nil {
			return nil, err
		}
	}
	return mongoDocsResultSet(docs, truncated)
}

// mongoDocsResultSet flattens documents into a ResultSet: columns appear in
// first-seen field order across all documents, cell values are normalized to
// JSON-friendly forms, and Raw carries the normalized documents as a JSON
// array (the "original" payload the agent can inspect when the tabular
// projection loses structure).
func mongoDocsResultSet(docs []bson.D, truncated bool) (*ResultSet, error) {
	rs := &ResultSet{Truncated: truncated, Engine: string(KindMongo)}
	colIdx := map[string]int{}
	normDocs := make([]map[string]any, len(docs))
	for i, doc := range docs {
		m := make(map[string]any, len(doc))
		for _, e := range doc {
			if _, ok := colIdx[e.Key]; !ok {
				colIdx[e.Key] = len(rs.Columns)
				rs.Columns = append(rs.Columns, Column{Name: e.Key})
			}
			m[e.Key] = normalizeMongoValue(e.Value)
		}
		normDocs[i] = m
	}
	for _, m := range normDocs {
		row := make([]any, len(rs.Columns))
		for key, idx := range colIdx {
			v, ok := m[key]
			if !ok {
				continue
			}
			cell, typ := mongoCell(v)
			row[idx] = cell
			if rs.Columns[idx].Type == "" {
				rs.Columns[idx].Type = typ
			}
		}
		rs.Rows = append(rs.Rows, row)
	}
	for i := range rs.Columns {
		if rs.Columns[i].Type == "" {
			rs.Columns[i].Type = "string"
		}
	}
	raw, err := json.Marshal(normDocs)
	if err != nil {
		return nil, fmt.Errorf("marshal mongo documents: %w", err)
	}
	rs.Raw = raw
	return rs, nil
}

// normalizeMongoValue converts BSON-specific types into JSON-friendly Go
// values: ObjectID → hex string, DateTime → time.Time (UTC), nested
// documents/arrays → plain maps/slices, Decimal128 → decimal string,
// Binary → base64.
func normalizeMongoValue(v any) any {
	switch x := v.(type) {
	case bson.D:
		m := make(map[string]any, len(x))
		for _, e := range x {
			m[e.Key] = normalizeMongoValue(e.Value)
		}
		return m
	case bson.M:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = normalizeMongoValue(val)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = normalizeMongoValue(val)
		}
		return m
	case bson.A:
		arr := make([]any, len(x))
		for i, val := range x {
			arr[i] = normalizeMongoValue(val)
		}
		return arr
	case []any:
		arr := make([]any, len(x))
		for i, val := range x {
			arr[i] = normalizeMongoValue(val)
		}
		return arr
	case bson.ObjectID:
		return x.Hex()
	case bson.DateTime:
		return x.Time().UTC()
	case bson.Decimal128:
		return x.String()
	case bson.Binary:
		return base64.StdEncoding.EncodeToString(x.Data)
	case bson.Timestamp:
		return fmt.Sprintf("Timestamp(%d,%d)", x.T, x.I)
	case nil, string, bool, int, int32, int64, float32, float64, time.Time:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// mongoCell maps a normalized value to its ResultSet cell and column type.
// Composite values stay as maps/slices (type "json") and serialize naturally.
func mongoCell(v any) (any, string) {
	switch x := v.(type) {
	case nil:
		return nil, ""
	case string:
		return x, "string"
	case bool:
		return x, "bool"
	case int, int32, int64, float32, float64:
		return x, "number"
	case time.Time:
		return x.Format(time.RFC3339), "time"
	case map[string]any, []any:
		return x, "json"
	default:
		return fmt.Sprintf("%v", x), "string"
	}
}

// mongoCountResultSet renders a single count as a one-column, one-row set.
func mongoCountResultSet(name string, n int64) (*ResultSet, error) {
	raw, err := json.Marshal(map[string]any{name: n})
	if err != nil {
		return nil, err
	}
	return &ResultSet{
		Columns: []Column{{Name: name, Type: "number"}},
		Rows:    [][]any{{n}},
		Engine:  string(KindMongo),
		Raw:     raw,
	}, nil
}

// mongoWriteResultSet renders a write acknowledgement: RowsAffected plus the
// driver counters / generated ids in Raw (no Columns/Rows — nothing tabular).
func mongoWriteResultSet(affected int64, detail map[string]any) (*ResultSet, error) {
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	return &ResultSet{RowsAffected: affected, Engine: string(KindMongo), Raw: raw}, nil
}
