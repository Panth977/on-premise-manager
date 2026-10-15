package devtools

// Remembered clients (§7.6): one pooled client per connection, created on first use and reused; closed after
// idleTimeoutSec idle, on disconnect, when the connection (or the preset of a new head) changes — the fingerprint of
// the resolved definition — and recreated once when an execution fails with a connection error.

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// client is a driver client of one kind.
type client struct {
	kind    string
	version string
	value   any // *pgxpool.Pool | *sql.DB | *gocql.Session | *redis.Client | mqtt.Client | *httpClients
	close   func()
}

type pooled struct {
	connID      string
	fingerprint string
	cl          *client
	connectedAt time.Time
	lastUsed    time.Time
}

type connState struct {
	lastError string
	lastUsed  time.Time
	errorAt   time.Time
}

type pool struct {
	s      *Service
	mu     sync.Mutex
	m      map[string]*pooled
	states map[string]*connState
	dialMu sync.Map // connID → *sync.Mutex (one dial at a time per connection)
}

func newPool(s *Service) *pool {
	return &pool{s: s, m: map[string]*pooled{}, states: map[string]*connState{}}
}

func (p *pool) dialLock(id string) *sync.Mutex {
	v, _ := p.dialMu.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// get returns the pooled client of a resolved connection (a changed fingerprint closes the old one).
func (p *pool) get(ctx context.Context, rc *resolvedConn) (*pooled, error) {
	id := rc.def.ID
	l := p.dialLock(id)
	l.Lock()
	defer l.Unlock()
	p.mu.Lock()
	pc := p.m[id]
	if pc != nil && pc.fingerprint != rc.fingerprint {
		delete(p.m, id)
		go pc.cl.close()
		pc = nil
	}
	if pc != nil {
		pc.lastUsed = time.Now()
		p.mu.Unlock()
		return pc, nil
	}
	p.mu.Unlock()
	cl, err := p.s.connect(ctx, rc)
	if err != nil {
		p.noteError(id, err)
		return nil, err
	}
	pc = &pooled{connID: id, fingerprint: rc.fingerprint, cl: cl, connectedAt: time.Now(), lastUsed: time.Now()}
	p.mu.Lock()
	p.m[id] = pc
	if st := p.states[id]; st != nil {
		st.lastError = ""
	}
	p.mu.Unlock()
	return pc, nil
}

func (p *pool) noteError(id string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.states[id]
	if st == nil {
		st = &connState{}
		p.states[id] = st
	}
	st.lastError, st.errorAt = err.Error(), time.Now()
}

func (p *pool) noteUsed(id string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.states[id]
	if st == nil {
		st = &connState{}
		p.states[id] = st
	}
	st.lastUsed = time.Now()
	if pc := p.m[id]; pc != nil {
		pc.lastUsed = st.lastUsed
	}
	if err == nil {
		st.lastError = ""
	}
}

// drop closes the pooled client of a connection (true = there was one).
func (p *pool) drop(id string) bool {
	p.mu.Lock()
	pc := p.m[id]
	delete(p.m, id)
	p.mu.Unlock()
	if pc != nil {
		pc.cl.close()
		return true
	}
	return false
}

// forget drops the client and the state (connection deleted).
func (p *pool) forget(id string) {
	p.drop(id)
	p.mu.Lock()
	delete(p.states, id)
	p.mu.Unlock()
}

func (p *pool) sweep() {
	idle := time.Duration(p.s.limits().IdleTimeoutSec) * time.Second
	var closeIDs []string
	p.mu.Lock()
	for id, pc := range p.m {
		if time.Since(pc.lastUsed) > idle && p.s.running.count(id) == 0 {
			closeIDs = append(closeIDs, id)
		}
	}
	p.mu.Unlock()
	for _, id := range closeIDs {
		p.drop(id)
	}
	// presets removed from the head / manual connections deleted elsewhere → close their clients
	h := p.s.h.Head()
	p.mu.Lock()
	var gone []string
	for id := range p.m {
		if strings.HasPrefix(id, "p:") {
			found := false
			if h != nil {
				for _, pr := range h.Presets {
					if presetID(pr.PresetSlug) == id {
						found = true
					}
				}
			}
			if !found {
				gone = append(gone, id)
			}
		}
	}
	p.mu.Unlock()
	for _, id := range gone {
		p.drop(id)
	}
}

func (p *pool) closeAll() {
	p.mu.Lock()
	all := p.m
	p.m = map[string]*pooled{}
	p.mu.Unlock()
	for _, pc := range all {
		pc.cl.close()
	}
}

// state is TS DevToolConnState of a connection.
func (p *pool) state(id string) ConnState {
	out := ConnState{Status: "idle", RunningQueries: p.s.running.count(id)}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.states[id]
	if pc := p.m[id]; pc != nil {
		out.Status = "connected"
		out.ConnectedAt = tsPtr(pc.connectedAt)
		out.LastUsedAt = tsPtr(pc.lastUsed)
		out.ServerVersion = strOrNil(pc.cl.version)
	} else if st != nil && st.lastError != "" {
		out.Status = "error"
	}
	if st != nil {
		if out.LastUsedAt == nil {
			out.LastUsedAt = tsPtr(st.lastUsed)
		}
		out.LastError = strOrNil(st.lastError)
	}
	return out
}

// isConnErr reports a transport-level failure (worth one reconnect), not a statement error.
func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	m := strings.ToLower(err.Error())
	for _, k := range []string{"conn closed", "connection reset", "broken pipe", "connection refused", "use of closed", "closed pool",
		"no connections", "gocql: no hosts available", "session has been closed", "client is closed", "not connected", "eof"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// withClient runs fn with the pooled client; a connection error drops the client and retries ONCE with a new one.
// retried = the second attempt ran.
func (s *Service) withClient(ctx context.Context, rc *resolvedConn, fn func(cl *client) error) (retried bool, err error) {
	pc, err := s.pool.get(ctx, rc)
	if err != nil {
		return false, err
	}
	err = fn(pc.cl)
	if err != nil && isConnErr(err) && ctx.Err() == nil {
		s.pool.drop(rc.def.ID)
		pc2, err2 := s.pool.get(ctx, rc)
		if err2 != nil {
			s.pool.noteError(rc.def.ID, err2)
			return true, err2
		}
		err = fn(pc2.cl)
		retried = true
	}
	if err != nil && isConnErr(err) {
		s.pool.noteError(rc.def.ID, err)
	} else {
		s.pool.noteUsed(rc.def.ID, err)
	}
	return retried, err
}

// connect creates a fresh client (pooled or for a Test).
func (s *Service) connect(ctx context.Context, rc *resolvedConn) (*client, error) {
	cctx, cancel := context.WithTimeout(ctx, rc.timeout(15))
	defer cancel()
	switch rc.def.Kind {
	case "postgres":
		return s.connectPG(cctx, rc)
	case "sqlite":
		return s.connectSQLite(cctx, rc)
	case "cassandra":
		return s.connectCQL(cctx, rc)
	case "redis":
		return s.connectRedis(cctx, rc)
	case "mqtt":
		return s.connectMQTT(cctx, rc, "", nil)
	case "http":
		return s.connectHTTP(rc), nil
	}
	return nil, errors.New("unknown kind " + rc.def.Kind)
}
