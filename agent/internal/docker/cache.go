package docker

// §21 #112 one shared view of the daemon: the agent's loops (health monitor 5 s, metrics 15 s, drift 60 s, uptime
// 30 s, the GUI) used to list and inspect every container independently — about 20 lists + 17 inspects per container
// per minute. StartCache subscribes to GET /events (one long-lived stream, reconnected with backoff) and caches
//   - container lists (per filter) and inspects (per id),
//   - image inspects (per ref),
// until an event says the object changed. Docker stays the only source of truth: an entry is dropped on any event
// about it, on any write through this client, after maxAge (a periodic full resync) and wholesale whenever the
// stream (re)connects or fails (events may have been missed). While the stream is down every call goes to the daemon.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// CacheMaxAge bounds how long an entry is served without an event (the periodic resync).
var CacheMaxAge = 30 * time.Second

// nowFn is the cache clock (tests advance it).
var nowFn = time.Now

type cachedList struct {
	at   time.Time
	list []ContainerSummary
}

type cachedInspect struct {
	at  time.Time
	cj  *ContainerJSON
	raw json.RawMessage
}

type cachedImage struct {
	at  time.Time
	img *Image
}

type stateCache struct {
	mu    sync.Mutex
	live  bool   // the events stream is connected: entries may be served
	gen   uint64 // bumped on every invalidation of lists (a fetch that raced an event is not stored)
	lists map[string]cachedList
	insp  map[string]cachedInspect // container id → inspect
	imgs  map[string]cachedImage   // image ref → inspect
	hits  int64
	subs  []chan Event
	// Connected is set by tests to wait for the stream
	connected chan struct{}
}

// CacheStats: hits served from the cache and whether the events stream is connected (self-metrics).
type CacheStats struct {
	Live bool  `json:"live"`
	Hits int64 `json:"hits"`
}

// StartCache enables the cache for this client and runs the events subscription until ctx ends.
func (c *Client) StartCache(ctx context.Context, logf func(msg string, kv ...any)) {
	sc := &stateCache{lists: map[string]cachedList{}, insp: map[string]cachedInspect{}, imgs: map[string]cachedImage{}, connected: make(chan struct{}, 1)}
	c.mu.Lock()
	c.cache = sc
	c.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil && logf != nil {
				logf("docker events loop panic", "panic", r)
			}
		}()
		backoff := time.Second
		for ctx.Err() == nil {
			started := time.Now()
			err := c.streamEvents(ctx, sc)
			sc.reset(false)
			if ctx.Err() != nil {
				return
			}
			if time.Since(started) > time.Minute {
				backoff = time.Second
			}
			if logf != nil && err != nil {
				logf("docker events stream ended; cache off until it reconnects", "error", err.Error(), "retryIn", backoff.String())
			}
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
}

// CacheStats reports the cache state (zero when off).
func (c *Client) CacheStats() CacheStats {
	sc := c.stateCache()
	if sc == nil {
		return CacheStats{}
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return CacheStats{Live: sc.live, Hits: sc.hits}
}

// SubscribeEvents returns a channel of daemon events (container, image, network) while the stream is up; the channel
// is buffered and events are dropped when the reader is slow. nil when the cache is off.
func (c *Client) SubscribeEvents() <-chan Event {
	sc := c.stateCache()
	if sc == nil {
		return nil
	}
	ch := make(chan Event, 64)
	sc.mu.Lock()
	sc.subs = append(sc.subs, ch)
	sc.mu.Unlock()
	return ch
}

func (c *Client) stateCache() *stateCache {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache
}

func (c *Client) streamEvents(ctx context.Context, sc *stateCache) error {
	f, _ := json.Marshal(map[string][]string{"type": {"container", "image", "network"}})
	resp, err := c.do(ctx, "GET", "/events", url.Values{"filters": {string(f)}}, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc.reset(true) // connected: anything cached before may have missed events
	select {
	case sc.connected <- struct{}{}:
	default:
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("events stream closed by the daemon")
			}
			return err
		}
		sc.apply(e)
	}
}

// reset drops every entry; live = whether entries may be stored from now on.
func (sc *stateCache) reset(live bool) {
	sc.mu.Lock()
	sc.live = live
	sc.gen++
	sc.lists = map[string]cachedList{}
	sc.insp = map[string]cachedInspect{}
	sc.imgs = map[string]cachedImage{}
	sc.mu.Unlock()
}

func (sc *stateCache) isLive() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.live
}

// ignored container actions: they change nothing an inspect or a list shows (health probes exec every few seconds)
func ignoredAction(a string) bool {
	if strings.HasPrefix(a, "exec_") {
		return true
	}
	switch a {
	case "top", "resize", "attach", "detach", "export", "copy", "archive-path", "extract-to-dir":
		return true
	}
	return false
}

func (sc *stateCache) apply(e Event) {
	sc.mu.Lock()
	switch e.Type {
	case "container":
		if !ignoredAction(e.Action) {
			sc.dropLocked(e.Actor.ID, e.Actor.Attributes["name"])
			sc.gen++
			sc.lists = map[string]cachedList{}
		}
	case "network":
		if id := e.Actor.Attributes["container"]; id != "" {
			sc.dropLocked(id, "")
		}
		sc.gen++
		sc.lists = map[string]cachedList{}
	case "image":
		sc.imgs = map[string]cachedImage{}
		sc.gen++
		sc.lists = map[string]cachedList{} // a list shows the image id of each container
	}
	subs := sc.subs
	sc.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// invalidate after a write through this client (the event follows, but a read right after the write must not see the
// old state). id "" = lists only.
func (c *Client) invalidate(id string) {
	sc := c.stateCache()
	if sc == nil {
		return
	}
	sc.mu.Lock()
	if id != "" {
		sc.dropLocked(id, id)
	}
	sc.gen++
	sc.lists = map[string]cachedList{}
	sc.mu.Unlock()
}

// dropLocked removes every inspect of one container, whatever key it was fetched by (full id, id prefix or name).
func (sc *stateCache) dropLocked(id, name string) {
	name = strings.TrimPrefix(name, "/")
	for k, v := range sc.insp {
		full := k
		if v.cj != nil {
			full = v.cj.ID
		}
		if k == id || (id != "" && (strings.HasPrefix(id, k) || strings.HasPrefix(full, id) || full == id)) || (name != "" && (k == name || (v.cj != nil && strings.TrimPrefix(v.cj.Name, "/") == name))) {
			delete(sc.insp, k)
		}
	}
}

func (c *Client) invalidateImages() {
	sc := c.stateCache()
	if sc == nil {
		return
	}
	sc.mu.Lock()
	sc.imgs = map[string]cachedImage{}
	sc.gen++
	sc.lists = map[string]cachedList{}
	sc.mu.Unlock()
}

func listKey(all bool, labels []string) string {
	l := append([]string(nil), labels...)
	sort.Strings(l)
	k := "0|"
	if all {
		k = "1|"
	}
	return k + strings.Join(l, "\x00")
}

// cachedListGet returns a copy of a fresh cached list, or the generation to store a fetched one under.
func (sc *stateCache) listGet(key string) ([]ContainerSummary, bool, uint64, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.live {
		return nil, false, 0, false
	}
	if v, ok := sc.lists[key]; ok && nowFn().Sub(v.at) < CacheMaxAge {
		sc.hits++
		return append([]ContainerSummary(nil), v.list...), true, sc.gen, true
	}
	return nil, false, sc.gen, true
}

func (sc *stateCache) listPut(key string, gen uint64, list []ContainerSummary) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.live && sc.gen == gen {
		sc.lists[key] = cachedList{at: nowFn(), list: append([]ContainerSummary(nil), list...)}
	}
}

func (sc *stateCache) inspGet(id string) (cachedInspect, bool, uint64, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.live {
		return cachedInspect{}, false, 0, false
	}
	if v, ok := sc.insp[id]; ok && nowFn().Sub(v.at) < CacheMaxAge {
		sc.hits++
		return v, true, sc.gen, true
	}
	return cachedInspect{}, false, sc.gen, true
}

// inspPut stores an inspect (merging the typed and raw forms of the same fetch generation).
func (sc *stateCache) inspPut(id string, gen uint64, cj *ContainerJSON, raw json.RawMessage) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.live || sc.gen != gen {
		return
	}
	v := sc.insp[id]
	if v.at.IsZero() || nowFn().Sub(v.at) >= CacheMaxAge {
		v = cachedInspect{at: nowFn()}
	}
	if cj != nil {
		v.cj = cj
	}
	if raw != nil {
		v.raw = raw
	}
	sc.insp[id] = v
}

func (sc *stateCache) imgGet(ref string) (*Image, bool, uint64, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.live {
		return nil, false, 0, false
	}
	if v, ok := sc.imgs[ref]; ok && nowFn().Sub(v.at) < CacheMaxAge {
		sc.hits++
		return v.img, true, sc.gen, true
	}
	return nil, false, sc.gen, true
}

func (sc *stateCache) imgPut(ref string, gen uint64, img *Image) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.live && sc.gen == gen {
		sc.imgs[ref] = cachedImage{at: nowFn(), img: img}
	}
}

func copyJSON(cj *ContainerJSON) *ContainerJSON {
	if cj == nil {
		return nil
	}
	cp := *cj
	return &cp
}
