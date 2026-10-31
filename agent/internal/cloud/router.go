package cloud

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// Endpoint is one row of CLOUD_API (api_gen.go, generated).
type Endpoint struct {
	Op, Method, Path, Auth, Perm, Body, Res string
	OK                                      int
}

// ReqLike is a request for Dispatch.
type ReqLike struct {
	Method string
	// URL: path from "/v1/" including "?query" exactly as sent
	URL     string
	Headers map[string]string // lower-case names
	Body    []byte
	IP      string
	Context context.Context
	// Mount: URL prefix before "/v1/" as received
	Mount string
}

// Ctx is what a handler gets.
type Ctx struct {
	Op     string
	Def    *Endpoint
	Method string
	// Path without query
	Path string
	// URL: path + query exactly as sent (what agents sign)
	URL     string
	Params  map[string]string
	Query   *doc.Search
	Headers map[string]string // lower-case names
	Body    []byte
	IP      string

	RequestID string
	Account   *Account   // auth: human
	Agent     *AgentAuth // auth: agent
	Context   context.Context
	Mount     string
	// Audit: audited requests only: handlers refine the root audit entry by setting it
	Audit *AuditHint

	parsed    any
	hasParsed bool
}

// JSON is c.json(): the parsed body ({} when empty; 422 V_TYPE when it is not JSON). Numbers are float64, objects *doc.Map.
func (c *Ctx) JSON() (any, error) {
	if !c.hasParsed {
		if len(c.Body) == 0 {
			c.parsed = doc.New()
		} else {
			v, err := doc.Parse(c.Body)
			if err != nil {
				return nil, E.Bad("", "V_TYPE", "body is not valid JSON")
			}
			c.parsed = v
		}
		c.hasParsed = true
	}
	return c.parsed, nil
}

// BodyMap is asObject(c.json()).
func (c *Ctx) BodyMap() (*doc.Map, error) {
	v, err := c.JSON()
	if err != nil {
		return nil, err
	}
	return AsObject(v)
}

// Handler answers one route: return any JSON-able value (*doc.Map keeps key order, doc.Obj builds one), a *Reply for
// anything else, or an error (*ApiError = its envelope, others 500 internal). (nil, nil) is the JSON body null.
type Handler func(c *Ctx) (any, error)

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handler{}
)

// Register adds the handler of an op; call it from init() of the route file. Unknown ops and duplicates panic.
func Register(op string, h Handler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	found := false
	for i := range cloudAPI {
		found = found || cloudAPI[i].Op == op
	}
	if !found {
		panic("cloud.Register: unknown op " + op)
	}
	if handlers[op] != nil {
		panic("cloud.Register: duplicate op " + op)
	}
	handlers[op] = h
}

// Handlers registers a table of handlers.
func Handlers(m map[string]Handler) {
	for k, v := range m {
		Register(k, v)
	}
}

// Unimplemented lists the ops without a handler, in table order.
func Unimplemented() []string {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	out := []string{}
	for i := range cloudAPI {
		if handlers[cloudAPI[i].Op] == nil {
			out = append(out, cloudAPI[i].Op)
		}
	}
	return out
}

// Endpoints returns the route table (a copy).
func Endpoints() []Endpoint { return append([]Endpoint{}, cloudAPI...) }

func handlerOf(op string) Handler {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	return handlers[op]
}

type route struct {
	def  *Endpoint
	re   *regexp.Regexp
	keys []string
	seg  int // literalScore = -len(keys)
}

var (
	routesOnce sync.Once
	routes     []route
	pathParam  = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)
)

func compileRoutes() []route {
	routesOnce.Do(func() {
		for i := range cloudAPI {
			d := &cloudAPI[i]
			var src strings.Builder
			src.WriteByte('^')
			var keys []string
			last := 0
			for _, m := range pathParam.FindAllStringSubmatchIndex(d.Path, -1) {
				src.WriteString(regexp.QuoteMeta(d.Path[last:m[0]]))
				src.WriteString("([^/]+)")
				keys = append(keys, d.Path[m[2]:m[3]])
				last = m[1]
			}
			src.WriteString(regexp.QuoteMeta(d.Path[last:]))
			src.WriteByte('$')
			routes = append(routes, route{def: d, re: regexp.MustCompile(src.String()), keys: keys, seg: -len(keys)})
		}
		sort.SliceStable(routes, func(a, b int) bool { return routes[a].seg > routes[b].seg })
	})
	return routes
}

// rate limit (per uid / per site, per instance, best effort)
type bucket struct {
	tokens float64
	at     int64
}

var (
	bucketMu sync.Mutex
	buckets  = map[string]*bucket{}
)

func rateLimit(key string) error {
	bucketMu.Lock()
	defer bucketMu.Unlock()
	now := time.Now().UnixMilli()
	b := buckets[key]
	if b == nil {
		b = &bucket{rateBurst, now}
		buckets[key] = b
	}
	b.tokens += float64(now-b.at) / 1000 * ratePerSec
	if b.tokens > rateBurst {
		b.tokens = rateBurst
	}
	b.at = now
	if b.tokens < 1 {
		retry := int((1 - b.tokens) / ratePerSec)
		if float64(retry) < (1-b.tokens)/ratePerSec {
			retry++
		}
		if retry < 1 {
			retry = 1
		}
		return E.New("rate-limited", "too many requests", doc.Obj("retryAfterSec", retry), map[string]string{"Retry-After": itoa(retry)})
	}
	b.tokens--
	if len(buckets) > 10000 {
		for k, v := range buckets {
			if now-v.at > 60000 {
				delete(buckets, k)
			}
		}
	}
	return nil
}

// IsCredentialOp: CREDENTIAL_OPS (no-store + per-uid fixed-minute limit).
func IsCredentialOp(op string) bool { return has(credentialOps, op) }

// RevealRate is REVEAL_RATE_PER_MINUTE; OPM_REVEAL_RATE overrides it (tests).
func RevealRate() int {
	if n, err := strconv.Atoi(os.Getenv("OPM_REVEAL_RATE")); err == nil && n > 0 {
		return n
	}
	return revealRatePerMinute
}

// CredentialRateLimit counts one value-returning request of uid in the current UTC minute (rateLimits/reveal_<uid>_<min>).
func CredentialRateLimit(uid string, now time.Time) error {
	n := now.UTC()
	win := n.Format("200601021504")
	winStart := time.Date(n.Year(), n.Month(), n.Day(), n.Hour(), n.Minute(), 0, 0, time.UTC).UnixMilli()
	ref := DB().Collection("rateLimits").Doc("reveal_" + strings.NewReplacer("/", "_", ".", "_").Replace(uid) + "_" + win)
	limit := RevealRate()
	count, err := driver.RunTx(DB(), func(tx *driver.Tx) (float64, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return 0, err
		}
		c := s.Data().Num("count") + 1
		tx.Set(ref, doc.Obj("uid", uid, "window", win, "count", c, "expireAt", doc.TimestampFromMillis(winStart+120_000)))
		return c, nil
	})
	if err != nil {
		return err
	}
	if count > float64(limit) {
		ms := winStart + 60_000 - n.UnixMilli()
		retry := int((ms + 999) / 1000)
		if retry < 1 {
			retry = 1
		}
		return E.New("rate-limited", fmt.Sprintf("too many credential requests (%d per minute); try again in %d s", limit, retry), doc.Obj("retryAfterSec", retry), map[string]string{"Retry-After": itoa(retry)})
	}
	return nil
}

const jsonLimit = 10 * 1024 * 1024

func jsonHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/json; charset=utf-8"}
}

// ErrorReply is errorReply: the error envelope.
func ErrorReply(err error, requestID string) *Reply {
	var ae *ApiError
	if errors.As(err, &ae) {
		h := jsonHeaders()
		for k, v := range ae.Headers {
			h[k] = v
		}
		return &Reply{ae.Status(), h, doc.MarshalString(doc.Obj("error", doc.Obj("code", ae.Code, "message", ae.Message, "details", ae.Details)))}
	}
	driver.Log.Error("internal error", "requestId", requestID, "error", err.Error())
	return &Reply{500, jsonHeaders(), doc.MarshalString(doc.Obj("error", doc.Obj("code", "internal", "message", "internal error", "details", doc.Obj("requestId", requestID))))}
}

// DispatchOpts of Dispatch.
type DispatchOpts struct {
	// PreAgent: relay: the inner request was verified by the relay handler
	PreAgent *AgentAuth
}

// RandomUUID is randomUUID.
func RandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// PrimeImageVolumes runs before every routed request (primeImageVolumes of core/catalog.ts; set by its port).
var PrimeImageVolumes func() error

// Dispatch routes one /v1 request. Never fails: errors become their envelope.
func Dispatch(req ReqLike, opts DispatchOpts) *Reply {
	requestID := RandomUUID()
	op := ""
	reply, err := safeRun(req, requestID, opts, &op)
	if err != nil {
		reply = ErrorReply(err, requestID)
	}
	if reply.Headers == nil {
		reply.Headers = map[string]string{}
	}
	if op != "" && IsCredentialOp(op) {
		reply.Headers["Cache-Control"] = "no-store"
		reply.Headers["Pragma"] = "no-cache"
	}
	reply.Headers["X-Request-Id"] = requestID
	reply.Headers["X-OPM-Server-Time"] = strconv.FormatInt(time.Now().Unix(), 10)
	return reply
}

func safeRun(req ReqLike, id string, opts DispatchOpts, op *string) (r *Reply, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return run(req, id, opts, op)
}

// call runs a handler; a panic is an internal error (so an audited request still gets its entry).
func call(h Handler, c *Ctx) (out any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return h(c)
}

func notImplemented(op string) *Reply {
	return &Reply{501, jsonHeaders(), doc.MarshalString(doc.Obj("error", doc.Obj("code", "not-implemented", "message", "op "+op+" is not ported yet", "details", doc.Obj("op", op))))}
}

func jsonReply(def *Endpoint, out any) (*Reply, error) {
	if r, ok := out.(*Reply); ok {
		return r, nil
	}
	b, err := doc.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &Reply{def.OK, jsonHeaders(), string(b)}, nil
}

func run(req ReqLike, requestID string, opts DispatchOpts, opOut *string) (*Reply, error) {
	path, rawQ, _ := strings.Cut(req.URL, "?")
	method := strings.ToUpper(req.Method)
	var found *route
	var m []string
	pathMatched := false
	rs := compileRoutes()
	for i := range rs {
		mm := rs[i].re.FindStringSubmatch(path)
		if mm == nil {
			continue
		}
		pathMatched = true
		if rs[i].def.Method == method {
			found = &rs[i]
			m = mm
			break
		}
	}
	if found == nil {
		if pathMatched {
			return nil, E.New("method-not-allowed", method+" is not allowed on "+path, nil, nil)
		}
		return nil, E.NotFound("no route " + method + " " + path)
	}
	params := map[string]string{}
	for i, k := range found.keys {
		v, err := doc.DecodeURIComponent(m[i+1])
		if err != nil {
			return nil, E.Bad(k, "V_FORMAT", "bad path parameter "+k)
		}
		params[k] = v
	}
	def := found.def
	*opOut = def.Op
	if PrimeImageVolumes != nil {
		if err := PrimeImageVolumes(); err != nil {
			return nil, err
		}
	}
	if def.Body == "json" && len(req.Body) > jsonLimit {
		return nil, E.TooLarge("JSON body larger than 10 MiB")
	}
	cx := req.Context
	if cx == nil {
		cx = context.Background()
	}
	c := &Ctx{Op: def.Op, Def: def, Method: method, Path: path, URL: req.URL, Params: params, Query: doc.ParseSearch(rawQ), Headers: req.Headers, Body: req.Body, IP: req.IP, RequestID: requestID, Context: cx, Mount: req.Mount}
	h := handlerOf(def.Op)
	if isAudited(def.Op, method, def.Auth, path) {
		// root audit hook: one entry per request, success or failure; a failed audit write never fails the request
		var pre *auditPre
		var out any
		err := func() error {
			var err error
			if c.Account, err = VerifyHuman(req.Headers["authorization"]); err != nil {
				return err
			}
			if err := rateLimit("u:" + c.Account.UID); err != nil {
				return err
			}
			if IsCredentialOp(def.Op) {
				if err := CredentialRateLimit(c.Account.UID, time.Now()); err != nil {
					return err
				}
			}
			if h == nil {
				return errNotPorted
			}
			pre = auditBegin(c)
			out, err = call(h, c)
			return err
		}()
		if errors.Is(err, errNotPorted) {
			return notImplemented(def.Op), nil
		}
		status := def.OK
		if r, ok := out.(*Reply); ok {
			status = r.Status
		}
		auditFinish(c, pre, out, err, status)
		if err != nil {
			return nil, err
		}
		return jsonReply(def, out)
	}
	switch def.Auth {
	case "human":
		var err error
		if c.Account, err = VerifyHuman(req.Headers["authorization"]); err != nil {
			return nil, err
		}
		if err := rateLimit("u:" + c.Account.UID); err != nil {
			return nil, err
		}
		if IsCredentialOp(def.Op) {
			if err := CredentialRateLimit(c.Account.UID, time.Now()); err != nil {
				return nil, err
			}
		}
	case "agent":
		if opts.PreAgent != nil {
			c.Agent = opts.PreAgent
		} else {
			var err error
			c.Agent, err = VerifyAgent(SignedParts{Method: method, Path: req.URL, Headers: req.Headers, Body: req.Body}, def.Op == "poll")
			if err != nil {
				return nil, err
			}
			if c.Agent.Agent.Truthy("relayVia") && def.Op != "relay" && def.Op != "heartbeat" {
				_ = Col.Agents(c.Agent.DeploymentID).Doc(c.Agent.SiteSlug).Update(doc.Obj("relayVia", nil))
			}
		}
		if err := rateLimit("s:" + c.Agent.DeploymentID + "/" + c.Agent.SiteSlug); err != nil {
			return nil, err
		}
	case "install":
		if err := rateLimit("i:" + req.IP); err != nil {
			return nil, err
		}
	}
	if h == nil {
		return notImplemented(def.Op), nil
	}
	out, err := call(h, c)
	if err != nil {
		return nil, err
	}
	return jsonReply(def, out)
}

var errNotPorted = errors.New("not ported")
