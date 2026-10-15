package devtools

// Redis (go-redis v9, RESP3): one pooled client per connection; executeRedis = one command (argv or a redis-cli
// line, classifyRedis); the key browser (SCAN + TYPE / TTL / MEMORY USAGE / length, values by type) and INFO /
// SLOWLOG are reads through the same client.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type quietRedisLog struct{}

func (quietRedisLog) Printf(context.Context, string, ...interface{}) {}

func init() { redis.SetLogger(quietRedisLog{}) } // the driver's pool chatter would go to stderr

func (s *Service) connectRedis(ctx context.Context, rc *resolvedConn) (*client, error) {
	db := 0
	if rc.database != "" {
		n, err := strconv.Atoi(strings.TrimSpace(rc.database))
		if err != nil || n < 0 || n > 15 {
			return nil, fmt.Errorf(`the Redis database is an index "0"–"15" (got %q)`, rc.database)
		}
		db = n
	}
	opt := &redis.Options{Addr: rc.addr, Username: rc.user, Password: rc.password, DB: db, Protocol: 3, ContextTimeoutEnabled: true,
		DialTimeout: 10 * time.Second, ReadTimeout: rc.timeout(30) + 5*time.Second, WriteTimeout: 30 * time.Second, PoolSize: 4, MaxRetries: -1, DialerRetries: 1,
		ClientName: "opm-devtools", DisableIdentity: true}
	if rc.def.Options.TLS {
		opt.TLSConfig = &tls.Config{InsecureSkipVerify: rc.def.Options.TLSInsecure, ServerName: deref(rc.def.Options.TLSServerName)}
	}
	cl := redis.NewClient(opt)
	if err := cl.Ping(ctx).Err(); err != nil {
		cl.Close()
		return nil, errors.New(rc.masker.Mask(err.Error()))
	}
	ver := ""
	if info, err := cl.Info(ctx, "server").Result(); err == nil {
		for _, l := range strings.Split(info, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "redis_version:"); ok {
				ver = "Redis " + v
			}
		}
	}
	return &client{kind: "redis", version: ver, value: cl, close: func() { cl.Close() }}, nil
}

// redisReply converts a go-redis RESP3 value into a RedisReply.
func redisReply(v any, maxCell int) RedisReply {
	cut := func(s string) any {
		if len(s) > maxCell {
			return CellTruncated{Truncated: true, Preview: TruncateUTF8(s, 2048).Text, SizeBytes: len(s), Fetchable: notFetchable()}
		}
		return s
	}
	switch x := v.(type) {
	case nil:
		return RedisReply{Type: "nil", Value: nil}
	case string:
		return RedisReply{Type: "string", Value: cut(x)}
	case int64:
		return RedisReply{Type: "integer", Value: x}
	case float64:
		return RedisReply{Type: "double", Value: normFloat(x)}
	case bool:
		return RedisReply{Type: "bool", Value: x}
	case *big.Int:
		return RedisReply{Type: "bignum", Value: x.String()}
	case redis.Error:
		return RedisReply{Type: "error", Value: x.Error()}
	case []any:
		out := make([]RedisReply, len(x))
		for i, e := range x {
			out[i] = redisReply(e, maxCell)
		}
		return RedisReply{Type: "array", Value: out}
	case map[any]any:
		keys := make([]string, 0, len(x))
		km := map[string]any{}
		for k, e := range x {
			ks := fmt.Sprint(k)
			keys = append(keys, ks)
			km[ks] = e
		}
		sort.Strings(keys)
		out := make([]RedisReply, 0, 2*len(keys))
		for _, k := range keys {
			out = append(out, RedisReply{Type: "string", Value: k}, redisReply(km[k], maxCell))
		}
		return RedisReply{Type: "map", Value: out}
	case error:
		return RedisReply{Type: "error", Value: x.Error()}
	}
	return RedisReply{Type: "string", Value: fmt.Sprint(v)}
}

func (s *Service) execRedis(ctx context.Context, cl *client, argv []string) (any, error) {
	rcl := cl.value.(*redis.Client)
	args := make([]any, len(argv))
	for i, a := range argv {
		args[i] = a
	}
	return rcl.Do(ctx, args...).Result()
}

func notFetchable() *bool { f := false; return &f }

func redisOf(cl *client) *redis.Client { return cl.value.(*redis.Client) }

// ───────────────────────── key browser

func (s *Service) withRedis(r *http.Request, fn func(ctx context.Context, rcl *redis.Client) (any, error)) (any, int, error) {
	d, e := s.findConn(r.PathValue("connectionId"))
	if e != nil {
		return nil, 0, e
	}
	if d.Kind != "redis" {
		return nil, 0, invalid("%s is a %s connection, not Redis", d.Name, d.Kind)
	}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		return nil, 0, e
	}
	s.noteTarget(d.ID, rc.targetText)
	ctx, cancel := context.WithTimeout(r.Context(), rc.timeout(s.limits().StatementTimeoutSec))
	defer cancel()
	var out any
	_, err := s.withClient(ctx, rc, func(cl *client) error {
		v, err := fn(ctx, redisOf(cl))
		out = v
		return err
	})
	if err != nil {
		var de *Error
		if errors.As(err, &de) {
			return nil, 0, de
		}
		return nil, 0, unavailable("%s", rc.masker.Mask(err.Error()))
	}
	return out, 200, nil
}

func lengthCmd(typ, key string) []any {
	switch typ {
	case "string":
		return []any{"STRLEN", key}
	case "hash":
		return []any{"HLEN", key}
	case "list":
		return []any{"LLEN", key}
	case "set":
		return []any{"SCARD", key}
	case "zset":
		return []any{"ZCARD", key}
	case "stream":
		return []any{"XLEN", key}
	}
	return nil
}

func (s *Service) handleRedisScan(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	q := r.URL.Query()
	pattern := q.Get("pattern")
	if pattern == "" {
		pattern = "*"
	}
	cursor, err := strconv.ParseUint(firstNonEmpty(q.Get("cursor"), "0"), 10, 64)
	if err != nil {
		return nil, 0, invalid("cursor must be a number")
	}
	count, _ := strconv.Atoi(q.Get("count"))
	if count <= 0 {
		count = 200
	}
	if count > 1000 {
		count = 1000
	}
	typ := q.Get("type")
	return s.withRedis(r, func(ctx context.Context, rcl *redis.Client) (any, error) {
		var keys []string
		var next uint64
		var err error
		if typ != "" {
			keys, next, err = rcl.ScanType(ctx, cursor, pattern, int64(count), typ).Result()
		} else {
			keys, next, err = rcl.Scan(ctx, cursor, pattern, int64(count)).Result()
		}
		if err != nil {
			return nil, err
		}
		if len(keys) > 1000 {
			keys = keys[:1000]
		}
		out := RedisScanResponse{Cursor: strconv.FormatUint(next, 10), Keys: []RedisKeyRow{}, Scanned: len(keys)}
		out.DBSize, _ = rcl.DBSize(ctx).Result()
		if len(keys) == 0 {
			return out, nil
		}
		pipe := rcl.Pipeline()
		types := make([]*redis.StatusCmd, len(keys))
		ttls := make([]*redis.DurationCmd, len(keys))
		mems := make([]*redis.IntCmd, len(keys))
		for i, k := range keys {
			types[i] = pipe.Type(ctx, k)
			ttls[i] = pipe.TTL(ctx, k)
			mems[i] = pipe.MemoryUsage(ctx, k)
		}
		_, _ = pipe.Exec(ctx)
		pipe2 := rcl.Pipeline()
		lens := make([]*redis.Cmd, len(keys))
		for i, k := range keys {
			if args := lengthCmd(types[i].Val(), k); args != nil {
				lens[i] = pipe2.Do(ctx, args...)
			}
		}
		_, _ = pipe2.Exec(ctx)
		for i, k := range keys {
			row := RedisKeyRow{Key: k, Type: types[i].Val(), TTLSec: ttlSec(ttls[i].Val())}
			if v, err := mems[i].Result(); err == nil {
				row.SizeBytes = &v
			}
			if lens[i] != nil {
				if v, err := lens[i].Int64(); err == nil {
					row.Length = &v
				}
			}
			out.Keys = append(out.Keys, row)
		}
		return out, nil
	})
}

func ttlSec(d time.Duration) int64 {
	if d < 0 {
		if d == -1 || d == -1*time.Nanosecond {
			return -1
		}
		if d == -2 || d == -2*time.Nanosecond {
			return -2
		}
		return int64(d / time.Second)
	}
	return int64(d / time.Second)
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Service) handleRedisKey(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	q := r.URL.Query()
	key := q.Get("key")
	if !q.Has("key") {
		return nil, 0, invalid("key is required")
	}
	cur := q.Get("cursor")
	count, _ := strconv.Atoi(q.Get("count"))
	if count <= 0 {
		count = 200
	}
	if count > 1000 {
		count = 1000
	}
	maxCell := s.limits().MaxCellBytes
	return s.withRedis(r, func(ctx context.Context, rcl *redis.Client) (any, error) {
		typ, err := rcl.Type(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		if typ == "none" {
			return nil, notFound("key %q does not exist", key)
		}
		ttl, _ := rcl.TTL(ctx, key).Result()
		out := RedisKeyValueResponse{Key: key, Type: typ, TTLSec: ttlSec(ttl)}
		if enc, err := rcl.ObjectEncoding(ctx, key).Result(); err == nil {
			out.Encoding = &enc
		}
		if args := lengthCmd(typ, key); args != nil {
			if n, err := rcl.Do(ctx, args...).Int64(); err == nil {
				out.Length = &n
			}
		}
		off, _ := strconv.ParseInt(firstNonEmpty(cur, "0"), 10, 64)
		setNext := func(n int64, more bool) {
			if more {
				v := strconv.FormatInt(n, 10)
				out.Next = &v
			}
		}
		cell := func(v string) any {
			if len(v) > maxCell {
				out.Truncated = true
				return CellTruncated{Truncated: true, Preview: TruncateUTF8(v, 2048).Text, SizeBytes: len(v), Fetchable: notFetchable()}
			}
			return v
		}
		switch typ {
		case "string":
			v, err := rcl.Get(ctx, key).Result()
			if err != nil {
				return nil, err
			}
			out.Value = cell(v)
		case "hash":
			c, _ := strconv.ParseUint(firstNonEmpty(cur, "0"), 10, 64)
			kv, next, err := rcl.HScan(ctx, key, c, "*", int64(count)).Result()
			if err != nil {
				return nil, err
			}
			pairs := [][2]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				pairs = append(pairs, [2]any{kv[i], cell(kv[i+1])})
			}
			out.Value = pairs
			if next != 0 {
				v := strconv.FormatUint(next, 10)
				out.Next = &v
			}
		case "list":
			vals, err := rcl.LRange(ctx, key, off, off+int64(count)-1).Result()
			if err != nil {
				return nil, err
			}
			l := []any{}
			for _, v := range vals {
				l = append(l, cell(v))
			}
			out.Value = l
			setNext(off+int64(len(vals)), out.Length != nil && off+int64(len(vals)) < *out.Length)
		case "set":
			c, _ := strconv.ParseUint(firstNonEmpty(cur, "0"), 10, 64)
			vals, next, err := rcl.SScan(ctx, key, c, "*", int64(count)).Result()
			if err != nil {
				return nil, err
			}
			l := []any{}
			for _, v := range vals {
				l = append(l, cell(v))
			}
			out.Value = l
			if next != 0 {
				v := strconv.FormatUint(next, 10)
				out.Next = &v
			}
		case "zset":
			zs, err := rcl.ZRangeWithScores(ctx, key, off, off+int64(count)-1).Result()
			if err != nil {
				return nil, err
			}
			l := [][2]any{}
			for _, z := range zs {
				l = append(l, [2]any{cell(fmt.Sprint(z.Member)), normFloat(z.Score)})
			}
			out.Value = l
			setNext(off+int64(len(zs)), out.Length != nil && off+int64(len(zs)) < *out.Length)
		case "stream":
			start := "-"
			if cur != "" {
				start = "(" + cur
			}
			msgs, err := rcl.XRangeN(ctx, key, start, "+", int64(count)).Result()
			if err != nil {
				return nil, err
			}
			l := []any{}
			for _, m := range msgs {
				l = append(l, map[string]any{"id": m.ID, "fields": normValue(m.Values)})
			}
			out.Value = l
			if len(msgs) == count {
				id := msgs[len(msgs)-1].ID
				out.Next = &id
			}
		default:
			if strings.HasPrefix(typ, "ReJSON") {
				v, err := rcl.Do(ctx, "JSON.GET", key).Text()
				if err == nil {
					out.Value = cell(v)
				}
			} else {
				out.Value = nil
			}
		}
		return out, nil
	})
}

func (s *Service) handleRedisInfo(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	return s.withRedis(r, func(ctx context.Context, rcl *redis.Client) (any, error) {
		info, err := rcl.Info(ctx, "everything").Result()
		if err != nil {
			info, err = rcl.Info(ctx).Result()
			if err != nil {
				return nil, err
			}
		}
		out := RedisInfoResponse{Sections: map[string]map[string]string{}, DBs: []RedisDB{}, Slowlog: []SlowlogEntry{}}
		sec := "default"
		for _, l := range strings.Split(info, "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if strings.HasPrefix(l, "#") {
				sec = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(l, "#")))
				continue
			}
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			if out.Sections[sec] == nil {
				out.Sections[sec] = map[string]string{}
			}
			out.Sections[sec][k] = v
			if k == "redis_version" {
				vv := "Redis " + v
				out.ServerVersion = &vv
			}
			if sec == "keyspace" && strings.HasPrefix(k, "db") {
				n, _ := strconv.Atoi(strings.TrimPrefix(k, "db"))
				d := RedisDB{DB: n}
				for _, kv := range strings.Split(v, ",") {
					a, b, _ := strings.Cut(kv, "=")
					x, _ := strconv.ParseInt(b, 10, 64)
					switch a {
					case "keys":
						d.Keys = x
					case "expires":
						d.Expires = x
					}
				}
				out.DBs = append(out.DBs, d)
			}
		}
		if logs, err := rcl.SlowLogGet(ctx, 20).Result(); err == nil {
			for _, e := range logs {
				out.Slowlog = append(out.Slowlog, SlowlogEntry{ID: e.ID, At: ts(e.Time), DurationUs: e.Duration.Microseconds(),
					Command: strings.Join(quoteRedisArgv(RedactRedisArgv(nonEmptyArgs(e.Args))), " ")})
			}
		}
		return out, nil
	})
}

func nonEmptyArgs(a []string) []string {
	if len(a) == 0 {
		return []string{""}
	}
	return a
}
