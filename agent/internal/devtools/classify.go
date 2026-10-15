package devtools

// Go ports of the pure helpers of shared/src/devtools.ts (splitSqlStatements, stripSqlComments, classifyStatement,
// classifyRedis, parseRedisCommandLine). Vectors: shared/testdata/devtools-vectors.json (classify_test.go).

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StatementClass is TS StatementClass.
type StatementClass struct {
	Write       bool     `json:"write"`
	Destructive bool     `json:"destructive"`
	Reasons     []string `json:"reasons"`
}

// RedisCommandClass is TS RedisCommandClass.
type RedisCommandClass struct {
	Blocked     bool     `json:"blocked"`
	Write       bool     `json:"write"`
	Destructive bool     `json:"destructive"`
	Reasons     []string `json:"reasons"`
}

var dollarTagRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

func isWordByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

// dollarTag returns the $tag$ starting at i ("" = none).
func dollarTag(text string, i int) string {
	if i > 0 && isWordByte(text[i-1]) {
		return ""
	}
	return dollarTagRe.FindString(text[i:])
}

// quotedEnd returns the index after the closing quote of a '…' / "…" / `…` starting at i (doubled quotes escape).
func quotedEnd(text string, i int) int {
	ch := text[i]
	n := len(text)
	j := i + 1
	for j < n {
		if text[j] == ch {
			if j+1 < n && text[j+1] == ch {
				j += 2
				continue
			}
			break
		}
		j++
	}
	if j+1 < n {
		return j + 1
	}
	return n
}

// jsTrim trims like String.prototype.trim (Unicode white space + BOM).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == rune(0xFEFF) })
}

// SplitSQLStatements is TS splitSqlStatements.
func SplitSQLStatements(text string) []string {
	out := []string{}
	var cur strings.Builder
	n := len(text)
	i := 0
	for i < n {
		ch := text[i]
		var nx byte
		if i+1 < n {
			nx = text[i+1]
		}
		if ch == '-' && nx == '-' {
			e := strings.IndexByte(text[i:], '\n')
			end := n
			if e >= 0 {
				end = i + e
			}
			cur.WriteString(text[i:end])
			i = end
			continue
		}
		if ch == '/' && nx == '*' {
			e := strings.Index(text[i+2:], "*/")
			end := n
			if e >= 0 {
				end = i + 2 + e + 2
			}
			cur.WriteString(text[i:end])
			i = end
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			end := quotedEnd(text, i)
			cur.WriteString(text[i:end])
			i = end
			continue
		}
		if ch == '$' {
			if tag := dollarTag(text, i); tag != "" {
				e := strings.Index(text[i+len(tag):], tag)
				end := n
				if e >= 0 {
					end = i + len(tag) + e + len(tag)
				}
				cur.WriteString(text[i:end])
				i = end
				continue
			}
		}
		if ch == ';' {
			if jsTrim(StripSQLComments(cur.String())) != "" {
				out = append(out, jsTrim(cur.String()))
			}
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(ch)
		i++
	}
	if jsTrim(StripSQLComments(cur.String())) != "" {
		out = append(out, jsTrim(cur.String()))
	}
	return out
}

// StripSQLComments is TS stripSqlComments: comments removed, string literals blanked (” ), quoted identifiers → ident.
func StripSQLComments(s string) string {
	var out strings.Builder
	n := len(s)
	i := 0
	for i < n {
		ch := s[i]
		var nx byte
		if i+1 < n {
			nx = s[i+1]
		}
		if ch == '-' && nx == '-' {
			e := strings.IndexByte(s[i:], '\n')
			if e < 0 {
				i = n
			} else {
				i += e
			}
			out.WriteByte(' ')
			continue
		}
		if ch == '/' && nx == '*' {
			e := strings.Index(s[i+2:], "*/")
			if e < 0 {
				i = n
			} else {
				i = i + 2 + e + 2
			}
			out.WriteByte(' ')
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			if ch == '\'' {
				out.WriteString(" '' ")
			} else {
				out.WriteString(" ident ")
			}
			i = quotedEnd(s, i)
			continue
		}
		if ch == '$' {
			if tag := dollarTag(s, i); tag != "" {
				e := strings.Index(s[i+len(tag):], tag)
				if e < 0 {
					i = n
				} else {
					i = i + len(tag) + e + len(tag)
				}
				out.WriteString(" '' ")
				continue
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

var readSQL = map[string]bool{}

func init() {
	for _, w := range []string{"SELECT", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "VALUES", "TABLE", "WITH", "PRAGMA", "USE", "SET", "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "LISTEN", "FETCH", "DECLARE", "CLOSE", "SAVEPOINT", "RELEASE", "LIST", "CONSISTENCY", "TRACING", "PAGING"} {
		readSQL[w] = true
	}
}

var (
	reWordSplit    = regexp.MustCompile(`[\s(;]+`)
	reDML          = regexp.MustCompile(`\b(INSERT|UPDATE|DELETE|MERGE)\b`)
	reInto         = regexp.MustCompile(`\bINTO\b`)
	reInsert       = regexp.MustCompile(`\bINSERT\b`)
	reForUpdate    = regexp.MustCompile(`\bFOR (UPDATE|NO KEY UPDATE|SHARE|KEY SHARE)\b`)
	reSetRW        = regexp.MustCompile(`^SET (SESSION )?(TRANSACTION|CHARACTERISTICS)?.*READ WRITE\b`)
	reAnalyze      = regexp.MustCompile(`\bANALY[SZ]E\b`)
	reDrop         = regexp.MustCompile(`\bDROP\b`)
	reRename       = regexp.MustCompile(`\bRENAME\b`)
	reDelUpd       = regexp.MustCompile(`\b(DELETE|UPDATE)\b`)
	reWhere        = regexp.MustCompile(`\bWHERE\b`)
	reFull         = regexp.MustCompile(`\bFULL\b`)
	reProgram      = regexp.MustCompile(`\bPROGRAM\b`)
	reCopyFile     = regexp.MustCompile(`\b(TO|FROM)\s+''`)
	reFileFunction = regexp.MustCompile(`\b(PG_READ_FILE|PG_READ_BINARY_FILE|LO_IMPORT|LO_EXPORT|PG_LS_DIR)\s*\(`)
)

// ClassifyStatement is TS classifyStatement (kind postgres | sqlite | cassandra).
func ClassifyStatement(kind, statement string) StatementClass {
	s := strings.Join(strings.FieldsFunc(StripSQLComments(statement), func(r rune) bool { return unicode.IsSpace(r) || r == rune(0xFEFF) }), " ")
	up := strings.ToUpper(s)
	var words []string
	for _, w := range reWordSplit.Split(up, -1) {
		if w != "" {
			words = append(words, w)
		}
	}
	first, second := "", ""
	if len(words) > 0 {
		first = words[0]
	}
	if len(words) > 1 {
		second = words[1]
	}
	reasons := []string{}
	write := !readSQL[first]
	has := func(re *regexp.Regexp) bool { return re.MatchString(up) }
	if first == "WITH" && has(reDML) {
		write = true
	}
	if first == "SELECT" && has(reInto) && !has(reInsert) && kind == "postgres" {
		write = true
	}
	if first == "SELECT" && has(reForUpdate) {
		write = true
	}
	if first == "PRAGMA" && strings.Contains(up, "=") {
		write = true
	}
	if first == "SET" && kind == "postgres" && has(reSetRW) {
		write = true
	}
	if first == "EXPLAIN" && has(reAnalyze) && has(reDML) {
		write = true
	}
	if first == "DROP" {
		if second == "MATERIALIZED" {
			reasons = append(reasons, "DROP MATERIALIZED VIEW")
		} else {
			reasons = append(reasons, "DROP "+second)
		}
	}
	if first == "TRUNCATE" {
		reasons = append(reasons, "TRUNCATE")
	}
	if first == "ALTER" && has(reDrop) {
		reasons = append(reasons, "ALTER … DROP")
	}
	if first == "ALTER" && has(reRename) {
		reasons = append(reasons, "ALTER … RENAME")
	}
	if first == "ALTER" && second == "SYSTEM" {
		reasons = append(reasons, "ALTER SYSTEM")
	}
	dmlFirst := first
	if first == "WITH" {
		dmlFirst = ""
		if m := reDelUpd.FindStringSubmatch(up); m != nil {
			dmlFirst = m[1]
		}
	}
	if dmlFirst == "DELETE" && !has(reWhere) {
		reasons = append(reasons, "DELETE without WHERE")
	}
	if dmlFirst == "UPDATE" && !has(reWhere) {
		reasons = append(reasons, "UPDATE without WHERE")
	}
	if first == "VACUUM" && has(reFull) {
		reasons = append(reasons, "VACUUM FULL")
	}
	if first == "REASSIGN" || (first == "DROP" && second == "OWNED") {
		if first == "REASSIGN" {
			reasons = []string{"REASSIGN OWNED"}
		} else {
			reasons = []string{"DROP OWNED"}
		}
	}
	if first == "REVOKE" {
		reasons = append(reasons, "REVOKE")
	}
	if kind == "postgres" && first == "COPY" && has(reProgram) {
		reasons = append(reasons, "COPY … PROGRAM (runs a command on the database server)")
	} else if kind == "postgres" && first == "COPY" && reCopyFile.MatchString(up) {
		reasons = append(reasons, "COPY to / from a server file")
	}
	if kind == "postgres" && has(reFileFunction) {
		reasons = append(reasons, "server file access function")
	}
	switch first {
	case "DELETE", "UPDATE", "INSERT", "MERGE", "UPSERT", "REPLACE":
		write = true
	}
	for i := range reasons {
		reasons[i] = jsTrim(reasons[i])
	}
	return StatementClass{Write: write || len(reasons) > 0, Destructive: len(reasons) > 0, Reasons: reasons}
}

// RedisBlocked is TS REDIS_BLOCKED.
var RedisBlocked = []string{"MONITOR", "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "UNSUBSCRIBE", "PUNSUBSCRIBE", "SUNSUBSCRIBE", "SHUTDOWN", "DEBUG", "MODULE", "SLAVEOF", "REPLICAOF", "MIGRATE", "SYNC", "PSYNC", "FAILOVER", "QUIT", "RESET", "HELLO", "SELECT", "WAIT", "WAITAOF", "CLIENT KILL", "CLIENT PAUSE", "CLIENT NO-EVICT", "CLIENT REPLY", "ACL SAVE", "ACL LOAD", "ACL SETUSER", "ACL DELUSER", "CONFIG REWRITE", "CLUSTER RESET", "BGREWRITEAOF", "BGSAVE", "SAVE", "LASTSAVE"}

// RedisRead is TS REDIS_READ.
var RedisRead = []string{"GET", "MGET", "GETRANGE", "STRLEN", "EXISTS", "TYPE", "TTL", "PTTL", "EXPIRETIME", "PEXPIRETIME", "KEYS", "SCAN", "HGET", "HMGET", "HGETALL", "HKEYS", "HVALS", "HLEN", "HEXISTS", "HSCAN", "HSTRLEN", "HRANDFIELD", "LRANGE", "LLEN", "LINDEX", "LPOS", "SMEMBERS", "SISMEMBER", "SMISMEMBER", "SCARD", "SSCAN", "SRANDMEMBER", "SINTER", "SUNION", "SDIFF", "SINTERCARD", "ZRANGE", "ZRANGEBYSCORE", "ZREVRANGE", "ZREVRANGEBYSCORE", "ZRANGEBYLEX", "ZREVRANGEBYLEX", "ZSCORE", "ZMSCORE", "ZCARD", "ZCOUNT", "ZLEXCOUNT", "ZRANK", "ZREVRANK", "ZSCAN", "ZRANDMEMBER", "ZINTER", "ZUNION", "ZDIFF", "ZINTERCARD", "XRANGE", "XREVRANGE", "XLEN", "XREAD", "XINFO", "XPENDING", "GEOPOS", "GEODIST", "GEOHASH", "GEOSEARCH", "GEORADIUS_RO", "GEORADIUSBYMEMBER_RO", "PFCOUNT", "BITCOUNT", "BITPOS", "GETBIT", "BITFIELD_RO", "DUMP", "OBJECT", "MEMORY", "RANDOMKEY", "DBSIZE", "INFO", "PING", "ECHO", "TIME", "LOLWUT", "COMMAND", "CONFIG GET", "CLIENT LIST", "CLIENT INFO", "CLIENT GETNAME", "CLIENT ID", "SLOWLOG", "LATENCY", "ROLE", "LCS", "SORT_RO", "JSON.GET", "JSON.MGET", "JSON.TYPE", "JSON.STRLEN", "JSON.ARRLEN", "JSON.OBJKEYS", "JSON.OBJLEN", "FT.SEARCH", "FT.INFO", "FT._LIST", "TS.GET", "TS.RANGE", "TS.MRANGE", "TS.INFO", "PUBSUB", "ACL WHOAMI", "ACL LIST", "ACL USERS", "ACL CAT", "FUNCTION LIST", "SCRIPT EXISTS"}

var redisContainers = []string{"CONFIG", "CLIENT", "ACL", "CLUSTER", "FUNCTION", "SCRIPT", "XINFO", "OBJECT", "MEMORY", "SLOWLOG", "LATENCY", "PUBSUB", "COMMAND", "XGROUP"}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// RedisCommandName is TS redisCommandName.
func RedisCommandName(argv []string) string {
	a := ""
	if len(argv) > 0 {
		a = strings.ToUpper(argv[0])
	}
	if inList(redisContainers, a) && len(argv) > 1 && argv[1] != "" {
		return a + " " + strings.ToUpper(argv[1])
	}
	return a
}

var rePatternKey = regexp.MustCompile(`[*?\[]`)

// ClassifyRedis is TS classifyRedis.
func ClassifyRedis(argv []string) RedisCommandClass {
	name := RedisCommandName(argv)
	first := ""
	if len(argv) > 0 {
		first = strings.ToUpper(argv[0])
	}
	blocked := inList(RedisBlocked, name) || inList(RedisBlocked, first)
	read := inList(RedisRead, name) || (name == first && inList(RedisRead, first)) ||
		(inList(RedisRead, first) && !inList([]string{"CONFIG", "CLIENT", "ACL", "CLUSTER", "FUNCTION", "SCRIPT", "XGROUP"}, first))
	reasons := []string{}
	if first == "FLUSHALL" || first == "FLUSHDB" {
		reasons = append(reasons, first)
	}
	if name == "CONFIG SET" {
		reasons = append(reasons, "CONFIG SET")
	}
	if name == "SCRIPT FLUSH" || name == "FUNCTION FLUSH" {
		reasons = append(reasons, name)
	}
	if first == "DEL" || first == "UNLINK" {
		for _, k := range argv[1:] {
			if rePatternKey.MatchString(k) {
				reasons = append(reasons, first+" with a pattern-like key")
				break
			}
		}
		if len(argv)-1 > 100 {
			reasons = append(reasons, first+" of more than 100 keys")
		}
	}
	if first == "EVAL" || first == "EVALSHA" || first == "FCALL" || first == "EVAL_RO" {
		reasons = append(reasons, first+" (script)")
	}
	return RedisCommandClass{Blocked: blocked, Write: !read || len(reasons) > 0, Destructive: len(reasons) > 0, Reasons: reasons}
}

func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == rune(0xFEFF) }

func isHex2(s string) bool {
	if len(s) != 2 {
		return false
	}
	_, err := strconv.ParseUint(s, 16, 8)
	return err == nil
}

// ParseRedisCommandLine is TS parseRedisCommandLine (nil, false = an unterminated quote). "\xHH" gives the raw byte
// (redis-cli semantics; the TS helper gives the code point U+00HH, the same for ASCII).
func ParseRedisCommandLine(line string) ([]string, bool) {
	out := []string{}
	rs := []rune(line)
	n := len(rs)
	i := 0
	for i < n {
		for i < n && isJSSpace(rs[i]) {
			i++
		}
		if i >= n {
			break
		}
		var w []byte
		quoted := false
		for i < n && !isJSSpace(rs[i]) {
			ch := rs[i]
			if ch == '"' {
				quoted = true
				i++
				closed := false
				for i < n {
					c := rs[i]
					if c == '\\' && i+1 < n {
						e := rs[i+1]
						if e == 'x' && i+4 <= n && isHex2(string(rs[i+2:i+4])) {
							v, _ := strconv.ParseUint(string(rs[i+2:i+4]), 16, 8)
							w = append(w, byte(v))
							i += 4
							continue
						}
						switch e {
						case 'n':
							w = append(w, '\n')
						case 'r':
							w = append(w, '\r')
						case 't':
							w = append(w, '\t')
						case 'b':
							w = append(w, '\b')
						case 'a':
							w = append(w, 7)
						default:
							w = utf8.AppendRune(w, e)
						}
						i += 2
						continue
					}
					if c == '"' {
						closed = true
						i++
						break
					}
					w = utf8.AppendRune(w, c)
					i++
				}
				if !closed {
					return nil, false
				}
				continue
			}
			if ch == '\'' {
				quoted = true
				i++
				closed := false
				for i < n {
					c := rs[i]
					if c == '\\' && i+1 < n && rs[i+1] == '\'' {
						w = append(w, '\'')
						i += 2
						continue
					}
					if c == '\'' {
						closed = true
						i++
						break
					}
					w = utf8.AppendRune(w, c)
					i++
				}
				if !closed {
					return nil, false
				}
				continue
			}
			w = utf8.AppendRune(w, ch)
			i++
		}
		if len(w) > 0 || quoted {
			out = append(out, string(w))
		}
	}
	return out, true
}
