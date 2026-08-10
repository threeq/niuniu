package dataconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConnector implements Connector for redis (Epic #345 Wave3 #357).
// Operations arrive as structured Command+Args (never a statement); Classify
// applies the W1 PoC §2.1 three-state whitelist (read / write / denied —
// anything else is unsupported) and reports every touched key in
// ResourceRef.Objects so the gateway's literal-key-prefix scope can gate it.
type RedisConnector struct{}

func NewRedisConnector() *RedisConnector { return &RedisConnector{} }

func (c *RedisConnector) Kind() SourceKind { return KindRedis }

// redisReadCmds / redisWriteCmds / redisDeniedCmds are the three-state
// whitelist from the W1 PoC conclusions §2.1
// (docs/superpowers/specs/2026-06-10-nosql-w1-poc-conclusions.md). A command
// in none of the three maps is rejected as unsupported — NOT treated as a
// write. FT.SEARCH/FT.AGGREGATE are the read-only RediSearch entry points
// (vector free tier, #357/#362); other FT.* (CREATE/ALTER/DROP...) stay
// unsupported. On a plain Redis without the module they fail at runtime with
// "unknown command", which is acceptable (W1 §4-4).
var (
	redisReadCmds = map[string]bool{
		"GET": true, "MGET": true, "GETRANGE": true, "STRLEN": true,
		"EXISTS": true, "TYPE": true, "TTL": true, "PTTL": true,
		"HGET": true, "HMGET": true, "HGETALL": true, "HLEN": true, "HSCAN": true,
		"LRANGE": true, "LLEN": true,
		"SMEMBERS": true, "SISMEMBER": true, "SCARD": true, "SSCAN": true,
		"ZRANGE": true, "ZRANGEBYSCORE": true, "ZSCORE": true, "ZCARD": true, "ZSCAN": true,
		"SCAN":      true,
		"FT.SEARCH": true, "FT.AGGREGATE": true,
	}
	redisWriteCmds = map[string]bool{
		"SET": true, "SETEX": true, "SETNX": true, "MSET": true, "APPEND": true,
		"INCR": true, "DECR": true, "INCRBY": true,
		"DEL": true, "EXPIRE": true, "PEXPIRE": true, "PERSIST": true,
		"HSET": true, "HDEL": true,
		"LPUSH": true, "RPUSH": true, "LPOP": true, "RPOP": true,
		"SADD": true, "SREM": true, "ZADD": true, "ZREM": true,
	}
	// redisDeniedCmds is the admin/dangerous plane, denied regardless of
	// access mode or scope (W1 §2.1). Matched on the FIRST token so
	// subcommand forms ("CONFIG GET") are caught. SELECT is here because the
	// logical db is injected via ConnConfig — the agent must not switch it.
	redisDeniedCmds = map[string]bool{
		"KEYS": true, "FLUSHALL": true, "FLUSHDB": true, "CONFIG": true,
		"SHUTDOWN": true, "DEBUG": true, "SCRIPT": true, "EVAL": true,
		"EVALSHA": true, "CLUSTER": true, "CLIENT": true, "MONITOR": true,
		"SUBSCRIBE": true, "PSUBSCRIBE": true, "SELECT": true, "SWAPDB": true,
		"MIGRATE": true,
	}
)

// Classify maps Command to read/write per the whitelist and statically
// extracts the touched keys into ResourceRef.Objects (for SCAN: the literal
// prefix of the mandatory MATCH pattern; for FT.*: the index name, which the
// prefix scope therefore also governs). ReferencesTables is always true —
// every whitelisted redis command targets keys, so an empty Objects must
// fail closed at the gateway.
func (c *RedisConnector) Classify(op Operation) (AccessMode, ResourceRef, error) {
	fields := strings.Fields(strings.ToUpper(op.Command))
	if len(fields) == 0 {
		return "", ResourceRef{}, errors.New("redis: command is required")
	}
	if redisDeniedCmds[fields[0]] {
		return "", ResourceRef{}, fmt.Errorf("%w: redis command %s is on the deny list", ErrDeniedByPolicy, fields[0])
	}
	if len(fields) != 1 {
		return "", ResourceRef{}, fmt.Errorf("%w: redis command %q is not whitelisted", ErrDeniedByPolicy, strings.Join(fields, " "))
	}
	cmd := fields[0]
	var mode AccessMode
	switch {
	case redisReadCmds[cmd]:
		mode = ModeRead
	case redisWriteCmds[cmd]:
		mode = ModeWrite
	default:
		return "", ResourceRef{}, fmt.Errorf("%w: redis command %q is not whitelisted", ErrDeniedByPolicy, cmd)
	}
	objects, err := redisCommandObjects(cmd, op.Args)
	if err != nil {
		return "", ResourceRef{}, err
	}
	return mode, ResourceRef{
		Database:         op.Database,
		Objects:          objects,
		CommandCls:       cmd,
		ReferencesTables: true,
	}, nil
}

// redisCommandObjects statically resolves the keys a whitelisted command
// touches. Multi-key commands report every key (any out-of-prefix key rejects
// the whole command at the gateway, W1 §2.1).
func redisCommandObjects(cmd string, args []string) ([]string, error) {
	switch cmd {
	case "MGET", "DEL", "EXISTS":
		if len(args) == 0 {
			return nil, fmt.Errorf("redis: %s requires at least one key", cmd)
		}
		return append([]string(nil), args...), nil
	case "MSET":
		if len(args) < 2 || len(args)%2 != 0 {
			return nil, errors.New("redis: MSET requires key/value pairs")
		}
		keys := make([]string, 0, len(args)/2)
		for i := 0; i < len(args); i += 2 {
			keys = append(keys, args[i])
		}
		return keys, nil
	case "SCAN":
		sa, err := parseScanArgs(args)
		if err != nil {
			return nil, err
		}
		return []string{globLiteralPrefix(sa.match)}, nil
	case "FT.SEARCH", "FT.AGGREGATE":
		if len(args) < 2 {
			return nil, fmt.Errorf("redis: %s requires an index and a query", cmd)
		}
		return []string{args[0]}, nil
	default:
		// Every remaining whitelisted command addresses a single key in
		// position 0 (HSCAN/SSCAN/ZSCAN scan WITHIN that key, so their MATCH
		// filters fields/members and needs no scope treatment).
		if len(args) == 0 {
			return nil, fmt.Errorf("redis: %s requires a key", cmd)
		}
		return []string{args[0]}, nil
	}
}

// scanArgs is the parsed option set of SCAN / {H,S,Z}SCAN.
type scanArgs struct {
	cursor   uint64
	match    string
	hasMatch bool
	count    int64
	typ      string
}

// parseScanArgs parses "cursor [MATCH p] [COUNT n] [TYPE t]" pairwise.
// MATCH is mandatory for SCAN (W1 §2.1: the pattern's literal prefix is the
// only statically checkable enumeration boundary; a bare SCAN is denied).
func parseScanArgs(args []string) (scanArgs, error) {
	sa, err := parseCursorOptions("SCAN", args, true)
	if err != nil {
		return sa, err
	}
	if !sa.hasMatch {
		return sa, errors.New("redis: SCAN requires a MATCH pattern (its literal prefix is checked against the key scope)")
	}
	return sa, nil
}

// parseCursorOptions parses the cursor + option tail shared by the scan
// family. allowType permits the TYPE option (SCAN only). Unknown options are
// rejected — fail closed rather than passing through unvetted syntax.
func parseCursorOptions(cmd string, args []string, allowType bool) (scanArgs, error) {
	var sa scanArgs
	if len(args) == 0 {
		return sa, fmt.Errorf("redis: %s requires a cursor", cmd)
	}
	cur, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return sa, fmt.Errorf("redis: %s cursor %q is not a number", cmd, args[0])
	}
	sa.cursor = cur
	for i := 1; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return sa, fmt.Errorf("redis: %s option %s requires a value", cmd, args[i])
		}
		switch strings.ToUpper(args[i]) {
		case "MATCH":
			sa.match, sa.hasMatch = args[i+1], true
		case "COUNT":
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || n <= 0 {
				return sa, fmt.Errorf("redis: %s COUNT %q is not a positive number", cmd, args[i+1])
			}
			sa.count = n
		case "TYPE":
			if !allowType {
				return sa, fmt.Errorf("redis: %s does not support TYPE", cmd)
			}
			sa.typ = args[i+1]
		default:
			return sa, fmt.Errorf("redis: unsupported %s option %q", cmd, args[i])
		}
	}
	return sa, nil
}

// globLiteralPrefix cuts a glob pattern at the first metacharacter
// (* ? [ and the \ escape — a conservative stop: a shorter prefix can only
// make the allow check stricter, never looser).
func globLiteralPrefix(p string) string {
	if i := strings.IndexAny(p, `*?[\`); i >= 0 {
		return p[:i]
	}
	return p
}

// client builds a short-lived client (same lifecycle as the SQL connectors;
// pooling is the registry Pool's future concern). ConnConfig.Database holds
// the logical db NUMBER for redis; the agent cannot SELECT away from it.
// Protocol is pinned to RESP2 so replies are flat arrays/scalars with one
// deterministic shape for normalization. MaxRetries is -1: go-redis retries
// commands on read timeouts by default, which would re-send non-idempotent
// writes (INCR/LPUSH/APPEND) whose first attempt already landed; readTimeout
// bounds each round trip instead.
func (c *RedisConnector) client(conn ConnConfig, readTimeout time.Duration) (*redis.Client, error) {
	port := conn.Port
	if port == 0 {
		port = 6379
	}
	db := 0
	if s := strings.TrimSpace(conn.Database); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("redis: database must be a non-negative db number, got %q", conn.Database)
		}
		db = n
	}
	return redis.NewClient(&redis.Options{
		Addr:        fmt.Sprintf("%s:%d", conn.Host, port),
		Username:    conn.User,
		Password:    conn.Password,
		DB:          db,
		Protocol:    2,
		MaxRetries:  -1,
		DialTimeout: 8 * time.Second,
		ReadTimeout: readTimeout,
	}), nil
}

func (c *RedisConnector) Ping(ctx context.Context, conn ConnConfig) error {
	client, err := c.client(conn, 8*time.Second)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return client.Ping(ctx).Err()
}

// Execute runs the (already gate-approved) command and normalizes the reply
// into a single-column or key/value two-column ResultSet (W1 §3 #357). The
// scan family loops its cursor server-side until exhaustion or RowLimit;
// everything else goes through a generic Do with per-command normalization.
func (c *RedisConnector) Execute(ctx context.Context, conn ConnConfig, op Operation) (*ResultSet, error) {
	// Defense in depth (mirrors ESConnector.Execute): re-run Classify so a
	// denied/unwhitelisted command can never execute even if a future caller
	// bypasses the gateway and hands an unclassified Operation to Execute.
	if _, _, err := c.Classify(op); err != nil {
		return nil, err
	}

	to := time.Duration(op.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = 15 * time.Second
	}
	client, err := c.client(conn, to)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	limit := op.RowLimit
	if limit <= 0 {
		limit = 1000
	}

	cmd := strings.ToUpper(strings.TrimSpace(op.Command))
	var rs *ResultSet
	switch cmd {
	case "SCAN":
		rs, err = execRedisScan(ctx, client, op.Args, limit)
	case "HSCAN", "SSCAN", "ZSCAN":
		rs, err = execRedisSubScan(ctx, client, cmd, op.Args, limit)
	default:
		// Known residual risk: go-redis materializes the full reply before we
		// truncate rows (e.g. LRANGE key 0 -1 on a huge list). RESP has no
		// protocol-level response cap, so the only hard bounds here are the
		// per-roundtrip ReadTimeout and the row truncation below; a byte-level
		// cap equivalent to ES's maxESResponseBytes is not implementable at
		// this layer.
		args := make([]any, 0, len(op.Args)+1)
		args = append(args, cmd)
		for _, a := range op.Args {
			args = append(args, a)
		}
		var v any
		v, err = client.Do(ctx, args...).Result()
		switch {
		case errors.Is(err, redis.Nil):
			// Missing key: zero rows, like an empty SQL result.
			rs, err = &ResultSet{Columns: []Column{{Name: "value", Type: "string"}}}, nil
		case err != nil:
		default:
			rs = normalizeRedisReply(cmd, op.Args, v, limit)
		}
	}
	if err != nil {
		return nil, err
	}
	rs.Engine = string(KindRedis)
	return rs, nil
}

// execRedisScan drives SCAN cursor loops to exhaustion (PoC note: single
// SCAN replies are routinely partial) and collects keys as a single "key"
// column, truncating at limit.
func execRedisScan(ctx context.Context, client *redis.Client, args []string, limit int) (*ResultSet, error) {
	sa, err := parseScanArgs(args)
	if err != nil {
		return nil, err
	}
	rs := &ResultSet{Columns: []Column{{Name: "key", Type: "string"}}}
	cursor := sa.cursor
	for {
		var keys []string
		var err error
		if sa.typ != "" {
			keys, cursor, err = client.ScanType(ctx, cursor, sa.match, sa.count, sa.typ).Result()
		} else {
			keys, cursor, err = client.Scan(ctx, cursor, sa.match, sa.count).Result()
		}
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if len(rs.Rows) >= limit {
				rs.Truncated = true
				return rs, nil
			}
			rs.Rows = append(rs.Rows, []any{k})
		}
		if cursor == 0 {
			return rs, nil
		}
	}
}

// execRedisSubScan drives HSCAN/SSCAN/ZSCAN ("key cursor [MATCH p] [COUNT n]")
// the same way; HSCAN yields field/value rows, ZSCAN member/score rows, SSCAN
// a single member column.
func execRedisSubScan(ctx context.Context, client *redis.Client, cmd string, args []string, limit int) (*ResultSet, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("redis: %s requires a key and a cursor", cmd)
	}
	key := args[0]
	sa, err := parseCursorOptions(cmd, args[1:], false)
	if err != nil {
		return nil, err
	}
	var rs *ResultSet
	paired := false
	switch cmd {
	case "HSCAN":
		rs = &ResultSet{Columns: []Column{{Name: "field", Type: "string"}, {Name: "value", Type: "string"}}}
		paired = true
	case "ZSCAN":
		rs = &ResultSet{Columns: []Column{{Name: "member", Type: "string"}, {Name: "score", Type: "string"}}}
		paired = true
	default: // SSCAN
		rs = &ResultSet{Columns: []Column{{Name: "member", Type: "string"}}}
	}
	cursor := sa.cursor
	for {
		var items []string
		var err error
		switch cmd {
		case "HSCAN":
			items, cursor, err = client.HScan(ctx, key, cursor, sa.match, sa.count).Result()
		case "ZSCAN":
			items, cursor, err = client.ZScan(ctx, key, cursor, sa.match, sa.count).Result()
		default:
			items, cursor, err = client.SScan(ctx, key, cursor, sa.match, sa.count).Result()
		}
		if err != nil {
			return nil, err
		}
		if paired {
			for i := 0; i+1 < len(items); i += 2 {
				if len(rs.Rows) >= limit {
					rs.Truncated = true
					return rs, nil
				}
				rs.Rows = append(rs.Rows, []any{items[i], items[i+1]})
			}
		} else {
			for _, m := range items {
				if len(rs.Rows) >= limit {
					rs.Truncated = true
					return rs, nil
				}
				rs.Rows = append(rs.Rows, []any{m})
			}
		}
		if cursor == 0 {
			return rs, nil
		}
	}
}

// normalizeRedisReply regularizes a raw RESP2 reply per command shape:
// positional multi-reads zip request keys/fields with the reply (k/v two
// columns), flat-pair replies fold into two columns, plain arrays become a
// single column, scalars one row — and any shape this table does not know
// (FT.* trees, module replies) falls back to a JSON "result" column with the
// full reply preserved in Raw.
func normalizeRedisReply(cmd string, args []string, v any, limit int) *ResultSet {
	switch cmd {
	case "MGET":
		if arr, ok := v.([]any); ok && len(arr) == len(args) {
			return zipRedisKV("key", args, arr, limit)
		}
	case "HMGET":
		if arr, ok := v.([]any); ok && len(args) >= 1 && len(arr) == len(args)-1 {
			return zipRedisKV("field", args[1:], arr, limit)
		}
	case "HGETALL":
		if arr, ok := v.([]any); ok && len(arr)%2 == 0 {
			return redisPairRows("field", "value", arr, limit)
		}
	case "ZRANGE", "ZRANGEBYSCORE":
		if arr, ok := v.([]any); ok {
			if hasRedisToken(args, "WITHSCORES") {
				if len(arr)%2 == 0 {
					return redisPairRows("member", "score", arr, limit)
				}
				break // malformed pairs: raw fallback
			}
			return redisListRows("member", arr, limit)
		}
	case "LRANGE", "SMEMBERS":
		if arr, ok := v.([]any); ok {
			return redisListRows("value", arr, limit)
		}
	}

	// Generic shapes: flat scalar array -> single column; scalar -> one row
	// (write acknowledgements keep the reply visible and record integer
	// replies as RowsAffected); anything nested -> JSON fallback.
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			switch e.(type) {
			case []any, map[any]any, map[string]any:
				return redisRawResult(v)
			}
		}
		return redisListRows("value", t, limit)
	case map[any]any, map[string]any:
		return redisRawResult(v)
	default:
		col := Column{Name: "value", Type: "string"}
		cell := normalizeCell(v, &col)
		rs := &ResultSet{Columns: []Column{col}, Rows: [][]any{{cell}}}
		if n, ok := v.(int64); ok && redisWriteCmds[cmd] {
			rs.RowsAffected = n
		}
		return rs
	}
}

// zipRedisKV pairs the request's key/field arguments with a positional reply
// array (MGET/HMGET); missing entries surface as null cells.
func zipRedisKV(keyCol string, keys []string, values []any, limit int) *ResultSet {
	rs := &ResultSet{Columns: []Column{{Name: keyCol, Type: "string"}, {Name: "value", Type: "string"}}}
	for i, k := range keys {
		if len(rs.Rows) >= limit {
			rs.Truncated = true
			break
		}
		rs.Rows = append(rs.Rows, []any{k, normalizeCell(values[i], &rs.Columns[1])})
	}
	return rs
}

// redisPairRows folds a flat [k1 v1 k2 v2 ...] reply into two columns.
func redisPairRows(c1, c2 string, arr []any, limit int) *ResultSet {
	rs := &ResultSet{Columns: []Column{{Name: c1, Type: "string"}, {Name: c2, Type: "string"}}}
	for i := 0; i+1 < len(arr); i += 2 {
		if len(rs.Rows) >= limit {
			rs.Truncated = true
			break
		}
		rs.Rows = append(rs.Rows, []any{
			normalizeCell(arr[i], &rs.Columns[0]),
			normalizeCell(arr[i+1], &rs.Columns[1]),
		})
	}
	return rs
}

// redisListRows renders a flat array as one single-column row per element.
func redisListRows(col string, arr []any, limit int) *ResultSet {
	rs := &ResultSet{Columns: []Column{{Name: col, Type: "string"}}}
	for _, e := range arr {
		if len(rs.Rows) >= limit {
			rs.Truncated = true
			break
		}
		rs.Rows = append(rs.Rows, []any{normalizeCell(e, &rs.Columns[0])})
	}
	return rs
}

// redisRawResult is the fallback for reply shapes without a tabular mapping:
// the JSON-encoded reply lands both in Raw and in a one-row "result" column.
func redisRawResult(v any) *ResultSet {
	b, err := json.Marshal(jsonifyRedisReply(v))
	if err != nil {
		b, _ = json.Marshal(fmt.Sprint(v))
	}
	return &ResultSet{
		Columns: []Column{{Name: "result", Type: "json"}},
		Rows:    [][]any{{string(b)}},
		Raw:     b,
	}
}

// jsonifyRedisReply makes a RESP reply json.Marshal-safe (map[any]any from
// RESP3-typed replies and []byte bulk strings are not directly marshalable).
func jsonifyRedisReply(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = jsonifyRedisReply(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = jsonifyRedisReply(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = jsonifyRedisReply(e)
		}
		return out
	default:
		return v
	}
}

// hasRedisToken reports whether args contains the token (case-insensitive) —
// used to detect reply-shape modifiers like WITHSCORES.
func hasRedisToken(args []string, token string) bool {
	for _, a := range args {
		if strings.EqualFold(a, token) {
			return true
		}
	}
	return false
}
