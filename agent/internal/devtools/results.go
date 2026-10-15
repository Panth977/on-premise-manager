package devtools

// Buffered results (paging / export / full cell values / HTTP bodies): kept 15 min, bound to the GUI session that ran
// the execution (or the forwarding site's session over the mesh).

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const resultTTL = 15 * time.Minute

type result struct {
	id          string
	owner       string // Caller.key()
	expires     time.Time
	columns     []ResultColumn
	rows        [][]any // full (normalized) values
	truncated   bool
	body        []byte // http
	contentType string
	fileName    string
}

type resultCache struct {
	mu sync.Mutex
	m  map[string]*result
}

func newResultCache() *resultCache { return &resultCache{m: map[string]*result{}} }

func (rc *resultCache) put(r *result) string {
	r.id = "r_" + randToken(16)
	r.expires = time.Now().Add(resultTTL)
	rc.mu.Lock()
	rc.m[r.id] = r
	// keep memory bounded: at most 200 results per agent (oldest first)
	if len(rc.m) > 200 {
		var all []*result
		for _, x := range rc.m {
			all = append(all, x)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].expires.Before(all[j].expires) })
		for _, x := range all[:len(all)-200] {
			delete(rc.m, x.id)
		}
	}
	rc.mu.Unlock()
	return r.id
}

func (rc *resultCache) get(id string, c Caller) (*result, *Error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	r := rc.m[id]
	if r == nil || time.Now().After(r.expires) || r.owner != c.key() {
		return nil, notFound("result %s is gone (results are kept 15 min for the session that ran them); run the query again", id)
	}
	r.expires = time.Now().Add(resultTTL)
	return r, nil
}

func (rc *resultCache) sweep() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for k, v := range rc.m {
		if time.Now().After(v.expires) {
			delete(rc.m, k)
		}
	}
}

// ───────────────────────── value normalization

// normValue turns a driver value into a JSON value (string | float64 | bool | nil | map | slice).
func normValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return x
	case bool:
		return x
	case int:
		return x
	case int8, int16, int32, uint8, uint16, uint32:
		return x
	case int64:
		if x > 1<<53 || x < -(1<<53) {
			return strconv.FormatInt(x, 10)
		}
		return x
	case uint64:
		if x > 1<<53 {
			return strconv.FormatUint(x, 10)
		}
		return x
	case float32:
		return normFloat(float64(x))
	case float64:
		return normFloat(x)
	case []byte:
		return "\\x" + fmt.Sprintf("%x", x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case time.Duration:
		return x.String()
	case json.RawMessage:
		var a any
		if json.Unmarshal(x, &a) == nil {
			return a
		}
		return string(x)
	case *big.Int:
		if x == nil {
			return nil
		}
		return x.String()
	case net.IP:
		return x.String()
	case netip.Addr:
		return x.String()
	case netip.Prefix:
		return x.String()
	case [16]byte: // uuid
		return fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normValue(e)
		}
		return out
	case fmt.Stringer:
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			return nil
		}
		return x.String()
	case json.Marshaler:
		if b, err := x.MarshalJSON(); err == nil {
			var a any
			if json.Unmarshal(b, &a) == nil {
				return a
			}
		}
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return normValue(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = normValue(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		out := map[string]any{}
		it := rv.MapRange()
		for it.Next() {
			out[fmt.Sprint(normValue(it.Key().Interface()))] = normValue(it.Value().Interface())
		}
		return out
	}
	if b, err := json.Marshal(v); err == nil {
		var a any
		if json.Unmarshal(b, &a) == nil {
			return a
		}
	}
	return fmt.Sprint(v)
}

func normFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}

// valueSize is the approximate byte size of a value (cell truncation, result budget).
func valueSize(v any) int {
	switch x := v.(type) {
	case nil:
		return 4
	case string:
		return len(x)
	case bool:
		return 5
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return len(b)
	}
	return 8
}

// valueText is the full text of a cell (export / download).
func valueText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

// cellsOf returns a page of rows with big cells cut (CellTruncated, row index absolute).
func cellsOf(rows [][]any, offset, maxCell int) [][]any {
	out := make([][]any, len(rows))
	for i, row := range rows {
		cells := make([]any, len(row))
		for j, v := range row {
			if n := valueSize(v); n > maxCell {
				t := valueText(v)
				cut := maxCell
				if cut > 2048 {
					cut = 2048
				}
				pv := TruncateUTF8(t, cut).Text
				cells[j] = CellTruncated{Truncated: true, Preview: pv, SizeBytes: n, Row: offset + i, Col: j}
			} else {
				cells[j] = v
			}
		}
		out[i] = cells
	}
	return out
}

// displayOf is the GUI renderer hint of a driver type name.
func displayOf(typ string) string {
	t := strings.ToLower(typ)
	switch {
	case t == "bool" || t == "boolean":
		return "bool"
	case strings.Contains(t, "json") || strings.HasPrefix(t, "map<") || strings.HasPrefix(t, "list<") || strings.HasPrefix(t, "set<") ||
		strings.HasPrefix(t, "tuple<") || strings.HasPrefix(t, "frozen<") || strings.HasPrefix(t, "_") || strings.HasSuffix(t, "[]"):
		return "json"
	case strings.Contains(t, "uuid"):
		return "uuid"
	case strings.Contains(t, "timestamp") || t == "date" || t == "time" || t == "timetz" || t == "datetime" || t == "interval":
		return "datetime"
	case t == "bytea" || t == "blob":
		return "bytes"
	case strings.Contains(t, "int") || strings.Contains(t, "float") || strings.Contains(t, "numeric") || strings.Contains(t, "decimal") ||
		strings.Contains(t, "double") || t == "real" || t == "counter" || t == "varint" || t == "money" || t == "oid":
		return "number"
	case strings.Contains(t, "char") || strings.Contains(t, "text") || t == "name" || t == "ascii" || t == "inet" || t == "citext" || t == "string":
		return "text"
	}
	return "other"
}

// rowBuffer collects rows up to bufferRows / maxResultBytes.
type rowBuffer struct {
	rows      [][]any
	bytes     int64
	maxRows   int
	maxBytes  int64
	truncated bool
}

func (b *rowBuffer) add(row []any) bool {
	if len(b.rows) >= b.maxRows || b.bytes >= b.maxBytes {
		b.truncated = true
		return false
	}
	n := int64(0)
	for _, v := range row {
		n += int64(valueSize(v))
	}
	b.bytes += n
	b.rows = append(b.rows, row)
	return true
}

// finishRows stores a rows result (when larger than the first page or with big cells) and fills the statement result.
func (s *Service) finishRows(sr *StatementResult, c Caller, cols []ResultColumn, b *rowBuffer, pageRows int) {
	lim := s.limits()
	sr.Columns = cols
	sr.TotalRows = len(b.rows)
	sr.Truncated = b.truncated
	first := b.rows
	if len(first) > pageRows {
		first = first[:pageRows]
	}
	sr.Rows = cellsOf(first, 0, lim.MaxCellBytes)
	if len(b.rows) > 0 {
		id := s.results.put(&result{owner: c.key(), columns: cols, rows: b.rows, truncated: b.truncated})
		sr.ResultID = &id
	}
}

// ───────────────────────── handlers

func (s *Service) handleResultPage(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	res, e := s.results.get(r.PathValue("resultId"), c)
	if e != nil {
		return nil, 0, e
	}
	q := r.URL.Query()
	off, _ := strconv.Atoi(q.Get("offset"))
	lim, _ := strconv.Atoi(q.Get("limit"))
	if lim <= 0 {
		lim = s.limits().MaxRows
	}
	if lim > 10000 {
		lim = 10000
	}
	if off < 0 {
		off = 0
	}
	end := off + lim
	if off > len(res.rows) {
		off = len(res.rows)
	}
	if end > len(res.rows) {
		end = len(res.rows)
	}
	return ResultPageResponse{ResultID: res.id, Offset: off, Rows: cellsOf(res.rows[off:end], off, s.limits().MaxCellBytes), TotalRows: len(res.rows), Truncated: res.truncated}, 200, nil
}

func (s *Service) handleResultValue(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	res, e := s.results.get(r.PathValue("resultId"), c)
	if e != nil {
		return nil, 0, e
	}
	row, err1 := strconv.Atoi(r.URL.Query().Get("row"))
	col, err2 := strconv.Atoi(r.URL.Query().Get("col"))
	if err1 != nil || err2 != nil || row < 0 || row >= len(res.rows) || col < 0 || col >= len(res.rows[row]) {
		return nil, 0, notFound("no cell %s,%s in result %s", r.URL.Query().Get("row"), r.URL.Query().Get("col"), res.id)
	}
	v := res.rows[row][col]
	ct, ext := "text/plain; charset=utf-8", "txt"
	body := []byte(valueText(v))
	if s, ok := v.(string); ok && strings.HasPrefix(s, "\\x") {
		if b, err := hexDecode(s[2:]); err == nil {
			body, ct, ext = b, "application/octet-stream", "bin"
		}
	} else if _, ok := v.(map[string]any); ok {
		ct, ext = "application/json", "json"
	} else if _, ok := v.([]any); ok {
		ct, ext = "application/json", "json"
	}
	name := "value"
	if col < len(res.columns) {
		name = safeFileName(res.columns[col].Name)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-row%d.%s"`, name, row+1, ext))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(200)
	_, _ = w.Write(body)
	return nil, 0, nil
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && (r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "value"
	}
	if b.Len() > 60 {
		return b.String()[:60]
	}
	return b.String()
}

func (s *Service) handleResultExport(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	res, e := s.results.get(r.PathValue("resultId"), c)
	if e != nil {
		return nil, 0, e
	}
	if res.body != nil {
		return nil, 0, invalid("an HTTP response is downloaded with …/body")
	}
	format := r.URL.Query().Get("format")
	stamp := time.Now().UTC().Format("20060102-150405")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="result-%s.csv"`, stamp))
		w.WriteHeader(200)
		cw := csv.NewWriter(w)
		head := make([]string, len(res.columns))
		for i, col := range res.columns {
			head[i] = col.Name
		}
		_ = cw.Write(head)
		for _, row := range res.rows {
			rec := make([]string, len(row))
			for i, v := range row {
				rec[i] = valueText(v)
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
	case "json", "ndjson":
		names := make([]string, len(res.columns))
		for i, col := range res.columns {
			names[i] = col.Name
		}
		obj := func(row []any) map[string]any {
			m := make(map[string]any, len(row))
			for i, v := range row {
				k := fmt.Sprintf("col%d", i+1)
				if i < len(names) {
					k = names[i]
				}
				m[k] = v
			}
			return m
		}
		if format == "json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="result-%s.json"`, stamp))
			w.WriteHeader(200)
			_, _ = w.Write([]byte("["))
			for i, row := range res.rows {
				if i > 0 {
					_, _ = w.Write([]byte(",\n"))
				}
				b, _ := json.Marshal(obj(row))
				_, _ = w.Write(b)
			}
			_, _ = w.Write([]byte("]\n"))
		} else {
			w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="result-%s.ndjson"`, stamp))
			w.WriteHeader(200)
			for _, row := range res.rows {
				b, _ := json.Marshal(obj(row))
				_, _ = w.Write(append(b, '\n'))
			}
		}
	default:
		return nil, 0, invalid("format must be csv | json | ndjson")
	}
	return nil, 0, nil
}

func (s *Service) handleResultBody(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	res, e := s.results.get(r.PathValue("resultId"), c)
	if e != nil {
		return nil, 0, e
	}
	if res.body == nil {
		return nil, 0, notFound("result %s is not an HTTP response", res.id)
	}
	ct := res.contentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, res.fileName))
	w.Header().Set("Content-Length", strconv.Itoa(len(res.body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.WriteHeader(200)
	_, _ = w.Write(res.body)
	return nil, 0, nil
}

// textOrBase64 returns (text, nil) for valid UTF-8, else (nil, base64).
func textOrBase64(b []byte) (*string, *string) {
	if utf8.Valid(b) {
		s := string(b)
		return &s, nil
	}
	s := base64.StdEncoding.EncodeToString(b)
	return nil, &s
}
