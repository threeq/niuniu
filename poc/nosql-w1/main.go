// NoSQL W1 PoC (issue #352, Epic #345 Wave1): verify pure-Go driver
// connectivity for MongoDB / Redis / Elasticsearch / Trino — one Ping plus one
// read-only operation each — against the docker-compose.yml services in this
// directory. Run with GOWORK=off (this module is intentionally outside
// go.work so the PoC deps never leak into the server module before Wave2/3).
//
//	GOWORK=off go run . [mongo|redis|es|trino ...]
//
// Endpoints override via env: POC_MONGO_URI, POC_REDIS_ADDR, POC_ES_URL,
// POC_TRINO_DSN.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/redis/go-redis/v9"
	_ "github.com/trinodb/trino-go-client/trino"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type check struct {
	name string
	fn   func(ctx context.Context) (string, error)
}

func main() {
	checks := []check{
		{"mongo", checkMongo},
		{"redis", checkRedis},
		{"es", checkES},
		{"trino", checkTrino},
	}
	only := map[string]bool{}
	for _, a := range os.Args[1:] {
		only[a] = true
	}
	failed := 0
	for _, c := range checks {
		if len(only) > 0 && !only[c.name] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		start := time.Now()
		detail, err := c.fn(ctx)
		cancel()
		if err != nil {
			failed++
			fmt.Printf("FAIL  %-6s %v\n", c.name, err)
			continue
		}
		fmt.Printf("PASS  %-6s %s (%.0fms)\n", c.name, detail, float64(time.Since(start).Milliseconds()))
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// checkMongo: Ping (primary read preference) + seeded find with a filter —
// the exact call shape the Wave3 mongo connector will use.
func checkMongo(ctx context.Context) (string, error) {
	uri := env("POC_MONGO_URI", "mongodb://localhost:27017")
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return "", fmt.Errorf("ping: %w", err)
	}
	coll := client.Database("poc").Collection("items")
	if _, err := coll.InsertOne(ctx, bson.M{"name": "alpha", "n": 1}); err != nil {
		return "", fmt.Errorf("seed insert: %w", err)
	}
	cur, err := coll.Find(ctx, bson.M{"name": "alpha"})
	if err != nil {
		return "", fmt.Errorf("find: %w", err)
	}
	var docs []bson.M
	if err := cur.All(ctx, &docs); err != nil {
		return "", fmt.Errorf("cursor: %w", err)
	}
	if len(docs) == 0 {
		return "", fmt.Errorf("find returned no documents")
	}
	return fmt.Sprintf("ping ok, find matched %d doc(s)", len(docs)), nil
}

// checkRedis: PING + GET + cursor SCAN with a prefix MATCH — SCAN (not KEYS)
// is the enumeration primitive the Wave3 redis connector whitelists.
func checkRedis(ctx context.Context) (string, error) {
	rdb := redis.NewClient(&redis.Options{Addr: env("POC_REDIS_ADDR", "localhost:6379")})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return "", fmt.Errorf("ping: %w", err)
	}
	if err := rdb.Set(ctx, "poc:greeting", "hello", time.Minute).Err(); err != nil {
		return "", fmt.Errorf("seed set: %w", err)
	}
	val, err := rdb.Get(ctx, "poc:greeting").Result()
	if err != nil {
		return "", fmt.Errorf("get: %w", err)
	}
	var keys []string
	var cursor uint64
	for {
		batch, next, err := rdb.Scan(ctx, cursor, "poc:*", 100).Result()
		if err != nil {
			return "", fmt.Errorf("scan: %w", err)
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("scan matched no keys")
	}
	return fmt.Sprintf("ping ok, GET=%q, SCAN poc:* -> %d key(s)", val, len(keys)), nil
}

// checkES: Info (ping equivalent) + index a doc + _search with a match query,
// parsing hits.hits the way the Wave3 connector will normalize ResultSet rows.
func checkES(ctx context.Context) (string, error) {
	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{env("POC_ES_URL", "http://localhost:9200")},
	})
	if err != nil {
		return "", fmt.Errorf("client: %w", err)
	}
	info, err := es.Info(es.Info.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("info: %w", err)
	}
	defer info.Body.Close()
	if info.IsError() {
		return "", fmt.Errorf("info: %s", info.String())
	}
	idx, err := es.Index("poc-items", strings.NewReader(`{"name":"alpha","n":1}`),
		es.Index.WithContext(ctx), es.Index.WithRefresh("true"))
	if err != nil {
		return "", fmt.Errorf("seed index: %w", err)
	}
	defer idx.Body.Close()
	if idx.IsError() {
		return "", fmt.Errorf("seed index: %s", idx.String())
	}
	res, err := es.Search(
		es.Search.WithContext(ctx),
		es.Search.WithIndex("poc-items"),
		es.Search.WithBody(strings.NewReader(`{"query":{"match":{"name":"alpha"}}}`)),
	)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return "", fmt.Errorf("search: %s", res.String())
	}
	var body struct {
		Hits struct {
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(body.Hits.Hits) == 0 {
		return "", fmt.Errorf("_search returned no hits")
	}
	return fmt.Sprintf("info ok, _search -> %d hit(s)", len(body.Hits.Hits)), nil
}

// checkTrino: database/sql Ping + a SELECT against the built-in tpch catalog —
// the same sql.Open("trino", dsn) path the Wave3 SQL-rail connector reuses.
func checkTrino(ctx context.Context) (string, error) {
	dsn := env("POC_TRINO_DSN", "http://poc@localhost:8080?catalog=tpch&schema=tiny")
	db, err := sql.Open("trino", dsn)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return "", fmt.Errorf("ping: %w", err)
	}
	rows, err := db.QueryContext(ctx, "SELECT name FROM tpch.tiny.nation ORDER BY name LIMIT 3")
	if err != nil {
		return "", fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", fmt.Errorf("scan: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("rows: %w", err)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("query returned no rows")
	}
	return fmt.Sprintf("ping ok, SELECT -> %s", strings.Join(names, ",")), nil
}
