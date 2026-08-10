package dataconn

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestRedisClassifyRead(t *testing.T) {
	c := NewRedisConnector()
	cases := []struct {
		cmd  string
		args []string
		want []string
	}{
		{"GET", []string{"cache:user:1"}, []string{"cache:user:1"}},
		{"get", []string{"cache:user:1"}, []string{"cache:user:1"}}, // case-insensitive command
		{"MGET", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"GETRANGE", []string{"k", "0", "10"}, []string{"k"}},
		{"STRLEN", []string{"k"}, []string{"k"}},
		{"EXISTS", []string{"a", "b"}, []string{"a", "b"}},
		{"TYPE", []string{"k"}, []string{"k"}},
		{"TTL", []string{"k"}, []string{"k"}},
		{"PTTL", []string{"k"}, []string{"k"}},
		{"HGET", []string{"h", "f"}, []string{"h"}},
		{"HMGET", []string{"h", "f1", "f2"}, []string{"h"}},
		{"HGETALL", []string{"h"}, []string{"h"}},
		{"HLEN", []string{"h"}, []string{"h"}},
		{"HSCAN", []string{"h", "0"}, []string{"h"}},
		{"LRANGE", []string{"l", "0", "-1"}, []string{"l"}},
		{"LLEN", []string{"l"}, []string{"l"}},
		{"SMEMBERS", []string{"s"}, []string{"s"}},
		{"SISMEMBER", []string{"s", "m"}, []string{"s"}},
		{"SCARD", []string{"s"}, []string{"s"}},
		{"SSCAN", []string{"s", "0"}, []string{"s"}},
		{"ZRANGE", []string{"z", "0", "-1"}, []string{"z"}},
		{"ZRANGEBYSCORE", []string{"z", "-inf", "+inf"}, []string{"z"}},
		{"ZSCORE", []string{"z", "m"}, []string{"z"}},
		{"ZCARD", []string{"z"}, []string{"z"}},
		{"ZSCAN", []string{"z", "0"}, []string{"z"}},
		// SCAN's scope object is the MATCH pattern's literal prefix.
		{"SCAN", []string{"0", "MATCH", "cache:*"}, []string{"cache:"}},
		{"SCAN", []string{"0", "match", "sess:abc"}, []string{"sess:abc"}},
		{"SCAN", []string{"0", "MATCH", "cache:u?id", "COUNT", "50"}, []string{"cache:u"}},
		{"SCAN", []string{"0", "MATCH", "a[bc]*"}, []string{"a"}},
		// FT.* read tier: the index name is the scoped object.
		{"FT.SEARCH", []string{"idx:docs", "@title:hello"}, []string{"idx:docs"}},
		{"FT.AGGREGATE", []string{"idx:docs", "*"}, []string{"idx:docs"}},
	}
	for _, tc := range cases {
		mode, ref, err := c.Classify(Operation{Command: tc.cmd, Args: tc.args, Database: "0"})
		if err != nil {
			t.Errorf("%s %v: unexpected error %v", tc.cmd, tc.args, err)
			continue
		}
		if mode != ModeRead {
			t.Errorf("%s: mode=%s, want read", tc.cmd, mode)
		}
		if !ref.ReferencesTables {
			t.Errorf("%s: ReferencesTables should be true", tc.cmd)
		}
		if ref.CommandCls != strings.ToUpper(tc.cmd) {
			t.Errorf("%s: CommandCls=%q", tc.cmd, ref.CommandCls)
		}
		if ref.Database != "0" {
			t.Errorf("%s: Database=%q, want passthrough", tc.cmd, ref.Database)
		}
		if got := strings.Join(ref.Objects, ","); got != strings.Join(tc.want, ",") {
			t.Errorf("%s %v: objects=%v, want %v", tc.cmd, tc.args, ref.Objects, tc.want)
		}
	}
}

func TestRedisClassifyWrite(t *testing.T) {
	c := NewRedisConnector()
	cases := []struct {
		cmd  string
		args []string
		want []string
	}{
		{"SET", []string{"k", "v"}, []string{"k"}},
		{"SETEX", []string{"k", "60", "v"}, []string{"k"}},
		{"SETNX", []string{"k", "v"}, []string{"k"}},
		{"MSET", []string{"a", "1", "b", "2"}, []string{"a", "b"}},
		{"APPEND", []string{"k", "v"}, []string{"k"}},
		{"INCR", []string{"k"}, []string{"k"}},
		{"DECR", []string{"k"}, []string{"k"}},
		{"INCRBY", []string{"k", "5"}, []string{"k"}},
		{"DEL", []string{"a", "b"}, []string{"a", "b"}},
		{"EXPIRE", []string{"k", "60"}, []string{"k"}},
		{"PEXPIRE", []string{"k", "60000"}, []string{"k"}},
		{"PERSIST", []string{"k"}, []string{"k"}},
		{"HSET", []string{"h", "f", "v"}, []string{"h"}},
		{"HDEL", []string{"h", "f"}, []string{"h"}},
		{"LPUSH", []string{"l", "v"}, []string{"l"}},
		{"RPUSH", []string{"l", "v"}, []string{"l"}},
		{"LPOP", []string{"l"}, []string{"l"}},
		{"RPOP", []string{"l"}, []string{"l"}},
		{"SADD", []string{"s", "m"}, []string{"s"}},
		{"SREM", []string{"s", "m"}, []string{"s"}},
		{"ZADD", []string{"z", "1", "m"}, []string{"z"}},
		{"ZREM", []string{"z", "m"}, []string{"z"}},
	}
	for _, tc := range cases {
		mode, ref, err := c.Classify(Operation{Command: tc.cmd, Args: tc.args})
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.cmd, err)
			continue
		}
		if mode != ModeWrite {
			t.Errorf("%s: mode=%s, want write", tc.cmd, mode)
		}
		if got := strings.Join(ref.Objects, ","); got != strings.Join(tc.want, ",") {
			t.Errorf("%s: objects=%v, want %v", tc.cmd, ref.Objects, tc.want)
		}
	}
}

func TestRedisClassifyDenied(t *testing.T) {
	c := NewRedisConnector()
	cases := []Operation{
		{Command: "KEYS", Args: []string{"*"}},
		{Command: "FLUSHALL"},
		{Command: "FLUSHDB"},
		{Command: "CONFIG", Args: []string{"GET", "maxmemory"}},
		{Command: "CONFIG GET", Args: []string{"maxmemory"}}, // subcommand folded into Command
		{Command: "config", Args: []string{"set", "dir", "/tmp"}},
		{Command: "SHUTDOWN"},
		{Command: "DEBUG", Args: []string{"SLEEP", "1"}},
		{Command: "SCRIPT", Args: []string{"LOAD", "return 1"}},
		{Command: "EVAL", Args: []string{"return 1", "0"}},
		{Command: "EVALSHA", Args: []string{"abc", "0"}},
		{Command: "CLUSTER", Args: []string{"INFO"}},
		{Command: "CLIENT", Args: []string{"LIST"}},
		{Command: "MONITOR"},
		{Command: "SUBSCRIBE", Args: []string{"ch"}},
		{Command: "PSUBSCRIBE", Args: []string{"ch.*"}},
		{Command: "SELECT", Args: []string{"1"}},
		{Command: "SWAPDB", Args: []string{"0", "1"}},
		{Command: "MIGRATE", Args: []string{"host", "6379", "k", "0", "5000"}},
	}
	for _, op := range cases {
		if _, _, err := c.Classify(op); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("%s: want denied error, got %v", op.Command, err)
		}
	}
}

func TestRedisClassifyUnsupported(t *testing.T) {
	c := NewRedisConnector()
	// Three-state whitelist: not read, not write, not explicitly denied ->
	// rejected as a policy refusal (never silently treated as a write).
	for _, cmd := range []string{"GETDEL", "RANDOMKEY", "SORT", "FT.CREATE", "FT.DROPINDEX", "OBJECT", "WAIT", "GET EXTRA"} {
		_, _, err := c.Classify(Operation{Command: cmd, Args: []string{"k"}})
		if err == nil || !errors.Is(err, ErrDeniedByPolicy) {
			t.Errorf("%s: want ErrDeniedByPolicy, got %v", cmd, err)
		}
	}
	if _, _, err := c.Classify(Operation{Command: "  "}); err == nil {
		t.Error("blank command: want error")
	}
}

func TestRedisClassifyBadShapes(t *testing.T) {
	c := NewRedisConnector()
	cases := []Operation{
		{Command: "GET"},                                          // missing key
		{Command: "MGET"},                                         // no keys
		{Command: "MSET", Args: []string{"a", "1", "b"}},          // odd pairs
		{Command: "SCAN", Args: []string{"0"}},                    // SCAN without MATCH is the KEYS loophole
		{Command: "SCAN", Args: []string{"0", "COUNT", "10"}},     // still no MATCH
		{Command: "SCAN", Args: []string{"abc", "MATCH", "a:*"}},  // non-numeric cursor
		{Command: "SCAN", Args: []string{"0", "MATCH"}},           // option without value
		{Command: "SCAN", Args: []string{"0", "BOGUS", "x"}},      // unknown option
		{Command: "FT.SEARCH", Args: []string{"idx"}},             // missing query
	}
	for _, op := range cases {
		if _, _, err := c.Classify(op); err == nil {
			t.Errorf("%s %v: want error, got mode/ref", op.Command, op.Args)
		}
	}
}

func TestGlobLiteralPrefix(t *testing.T) {
	cases := map[string]string{
		"cache:*":    "cache:",
		"cache:u?":   "cache:u",
		"a[bc]d":     "a",
		`esc\*ape`:   "esc",
		"nometa":     "nometa",
		"*":          "",
	}
	for in, want := range cases {
		if got := globLiteralPrefix(in); got != want {
			t.Errorf("globLiteralPrefix(%q)=%q, want %q", in, got, want)
		}
	}
}

// --- Execute against miniredis ---

func redisTestConn(t *testing.T) (*miniredis.Miniredis, ConnConfig) {
	t.Helper()
	mr := miniredis.RunT(t)
	port, err := strconv.Atoi(mr.Port())
	if err != nil {
		t.Fatalf("miniredis port: %v", err)
	}
	return mr, ConnConfig{Kind: KindRedis, Host: mr.Host(), Port: port}
}

func TestRedisPing(t *testing.T) {
	c := NewRedisConnector()
	_, conn := redisTestConn(t)
	if err := c.Ping(context.Background(), conn); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestRedisExecuteGet(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.Set("k", "hello")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "GET", Args: []string{"k"}})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if rs.Engine != "redis" || len(rs.Columns) != 1 || rs.Columns[0].Name != "value" {
		t.Fatalf("shape: %+v", rs)
	}
	if len(rs.Rows) != 1 || rs.Rows[0][0] != "hello" {
		t.Fatalf("rows: %v", rs.Rows)
	}

	// Missing key -> zero rows (redis.Nil is not an error).
	rs, err = c.Execute(context.Background(), conn, Operation{Command: "GET", Args: []string{"absent"}})
	if err != nil {
		t.Fatalf("GET absent: %v", err)
	}
	if len(rs.Rows) != 0 {
		t.Fatalf("GET absent rows: %v", rs.Rows)
	}
}

func TestRedisExecuteMGet(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.Set("a", "1")
	mr.Set("b", "2")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "MGET", Args: []string{"a", "missing", "b"}})
	if err != nil {
		t.Fatalf("MGET: %v", err)
	}
	if len(rs.Columns) != 2 || rs.Columns[0].Name != "key" || rs.Columns[1].Name != "value" {
		t.Fatalf("columns: %+v", rs.Columns)
	}
	if len(rs.Rows) != 3 {
		t.Fatalf("rows: %v", rs.Rows)
	}
	if rs.Rows[0][1] != "1" || rs.Rows[1][1] != nil || rs.Rows[2][1] != "2" {
		t.Fatalf("values: %v", rs.Rows)
	}
}

func TestRedisExecuteHGetAll(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.HSet("h", "f1", "v1", "f2", "v2")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "HGETALL", Args: []string{"h"}})
	if err != nil {
		t.Fatalf("HGETALL: %v", err)
	}
	if len(rs.Columns) != 2 || rs.Columns[0].Name != "field" {
		t.Fatalf("columns: %+v", rs.Columns)
	}
	got := map[string]string{}
	for _, r := range rs.Rows {
		got[r[0].(string)] = r[1].(string)
	}
	if got["f1"] != "v1" || got["f2"] != "v2" || len(got) != 2 {
		t.Fatalf("rows: %v", rs.Rows)
	}
}

func TestRedisExecuteListAndZSet(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.RPush("l", "x", "y")
	mr.ZAdd("z", 1, "a")
	mr.ZAdd("z", 2, "b")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "LRANGE", Args: []string{"l", "0", "-1"}})
	if err != nil {
		t.Fatalf("LRANGE: %v", err)
	}
	if len(rs.Columns) != 1 || len(rs.Rows) != 2 || rs.Rows[0][0] != "x" {
		t.Fatalf("LRANGE rows: %v", rs.Rows)
	}

	rs, err = c.Execute(context.Background(), conn, Operation{Command: "ZRANGE", Args: []string{"z", "0", "-1", "WITHSCORES"}})
	if err != nil {
		t.Fatalf("ZRANGE: %v", err)
	}
	if len(rs.Columns) != 2 || rs.Columns[0].Name != "member" || rs.Columns[1].Name != "score" {
		t.Fatalf("ZRANGE columns: %+v", rs.Columns)
	}
	if len(rs.Rows) != 2 || rs.Rows[0][0] != "a" || rs.Rows[1][0] != "b" {
		t.Fatalf("ZRANGE rows: %v", rs.Rows)
	}

	rs, err = c.Execute(context.Background(), conn, Operation{Command: "ZRANGE", Args: []string{"z", "0", "-1"}})
	if err != nil {
		t.Fatalf("ZRANGE plain: %v", err)
	}
	if len(rs.Columns) != 1 || len(rs.Rows) != 2 {
		t.Fatalf("ZRANGE plain rows: %v", rs.Rows)
	}
}

func TestRedisExecuteWrites(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "SET", Args: []string{"k", "v"}})
	if err != nil {
		t.Fatalf("SET: %v", err)
	}
	if len(rs.Rows) != 1 || rs.Rows[0][0] != "OK" {
		t.Fatalf("SET rows: %v", rs.Rows)
	}
	if got, _ := mr.Get("k"); got != "v" {
		t.Fatalf("SET did not land: %q", got)
	}

	mr.Set("d1", "x")
	mr.Set("d2", "x")
	rs, err = c.Execute(context.Background(), conn, Operation{Command: "DEL", Args: []string{"d1", "d2", "d3"}})
	if err != nil {
		t.Fatalf("DEL: %v", err)
	}
	if rs.RowsAffected != 2 {
		t.Fatalf("DEL RowsAffected=%d, want 2", rs.RowsAffected)
	}
}

func TestRedisExecuteScan(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	for i := 0; i < 5; i++ {
		mr.Set("cache:"+strconv.Itoa(i), "v")
	}
	mr.Set("other:1", "v")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "SCAN", Args: []string{"0", "MATCH", "cache:*", "COUNT", "2"}})
	if err != nil {
		t.Fatalf("SCAN: %v", err)
	}
	if len(rs.Columns) != 1 || rs.Columns[0].Name != "key" {
		t.Fatalf("SCAN columns: %+v", rs.Columns)
	}
	var keys []string
	for _, r := range rs.Rows {
		keys = append(keys, r[0].(string))
	}
	sort.Strings(keys)
	if len(keys) != 5 || keys[0] != "cache:0" || keys[4] != "cache:4" {
		t.Fatalf("SCAN keys: %v", keys)
	}

	// RowLimit truncates the cursor loop.
	rs, err = c.Execute(context.Background(), conn, Operation{Command: "SCAN", Args: []string{"0", "MATCH", "cache:*"}, RowLimit: 3})
	if err != nil {
		t.Fatalf("SCAN limited: %v", err)
	}
	if len(rs.Rows) != 3 || !rs.Truncated {
		t.Fatalf("SCAN limited: rows=%d truncated=%v", len(rs.Rows), rs.Truncated)
	}
}

func TestRedisExecuteHScan(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.HSet("h", "f1", "v1", "f2", "v2")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "HSCAN", Args: []string{"h", "0"}})
	if err != nil {
		t.Fatalf("HSCAN: %v", err)
	}
	if len(rs.Columns) != 2 || rs.Columns[0].Name != "field" {
		t.Fatalf("HSCAN columns: %+v", rs.Columns)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("HSCAN rows: %v", rs.Rows)
	}
}

func TestRedisExecuteSScan(t *testing.T) {
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.SAdd("s", "m1", "m2", "m3")

	rs, err := c.Execute(context.Background(), conn, Operation{Command: "SSCAN", Args: []string{"s", "0"}})
	if err != nil {
		t.Fatalf("SSCAN: %v", err)
	}
	if len(rs.Columns) != 1 || rs.Columns[0].Name != "member" {
		t.Fatalf("SSCAN columns: %+v", rs.Columns)
	}
	var members []string
	for _, r := range rs.Rows {
		members = append(members, r[0].(string))
	}
	sort.Strings(members)
	if len(members) != 3 || members[0] != "m1" || members[2] != "m3" {
		t.Fatalf("SSCAN members: %v", members)
	}
}

func TestRedisExecuteBadDatabase(t *testing.T) {
	c := NewRedisConnector()
	_, conn := redisTestConn(t)
	conn.Database = "not-a-number"
	if _, err := c.Execute(context.Background(), conn, Operation{Command: "GET", Args: []string{"k"}}); err == nil {
		t.Fatal("want error for non-numeric database")
	}
	if err := c.Ping(context.Background(), conn); err == nil {
		t.Fatal("want ping error for non-numeric database")
	}
}

func TestNormalizeRedisReplyFallback(t *testing.T) {
	// FT.SEARCH-style nested reply has no tabular mapping: JSON fallback with
	// Raw preserved.
	v := []any{int64(1), "doc:1", []any{"title", "hello"}}
	rs := normalizeRedisReply("FT.SEARCH", []string{"idx", "*"}, v, 100)
	if len(rs.Columns) != 1 || rs.Columns[0].Name != "result" || rs.Columns[0].Type != "json" {
		t.Fatalf("fallback columns: %+v", rs.Columns)
	}
	if len(rs.Rows) != 1 || rs.Raw == nil {
		t.Fatalf("fallback rows/raw: %+v", rs)
	}
	if !strings.Contains(rs.Rows[0][0].(string), "doc:1") {
		t.Fatalf("fallback content: %v", rs.Rows[0][0])
	}
}

func TestRedisExecuteRevalidates(t *testing.T) {
	// Defense in depth: Execute re-runs Classify, so a denied / unwhitelisted
	// command handed directly to Execute is rejected before any I/O — even
	// against a live server.
	c := NewRedisConnector()
	mr, conn := redisTestConn(t)
	mr.Set("keep", "1")

	for _, op := range []Operation{
		{Command: "FLUSHALL"},
		{Command: "KEYS", Args: []string{"*"}},
		{Command: "GETDEL", Args: []string{"keep"}},
		{Command: "CONFIG GET", Args: []string{"maxmemory"}},
	} {
		if _, err := c.Execute(context.Background(), conn, op); err == nil || !errors.Is(err, ErrDeniedByPolicy) {
			t.Errorf("%s: want ErrDeniedByPolicy from Execute, got %v", op.Command, err)
		}
	}
	if got, _ := mr.Get("keep"); got != "1" {
		t.Fatalf("denied command had a side effect: keep=%q", got)
	}
}
