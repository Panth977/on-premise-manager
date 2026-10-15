package devtools

// MQTT (paho): a pooled publish client per connection + live subscription sessions (own client each, ≤ 4 per GUI
// session) streamed as SSE with a single-use ticket; topics seen (last 1000 per connection, since the agent started).

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"opm/agent/internal/model"
)

func (s *Service) mqttOptions(rc *resolvedConn, suffix string) *mqtt.ClientOptions {
	scheme := "tcp"
	if rc.def.Options.TLS {
		scheme = "ssl"
	}
	id := rc.clientID
	if id == "" {
		id = "opm-devtools-" + s.h.Site() + "-" + randToken(6)
	}
	if suffix != "" {
		id += "-" + suffix
		if len(id) > 65535 {
			id = id[:65535]
		}
	}
	o := mqtt.NewClientOptions().AddBroker(scheme + "://" + rc.addr).SetClientID(id).SetConnectTimeout(10 * time.Second).
		SetWriteTimeout(30 * time.Second).SetOrderMatters(false).SetAutoReconnect(false).SetConnectRetry(false)
	if rc.user != "" {
		o.SetUsername(rc.user)
	}
	if rc.password != "" {
		o.SetPassword(rc.password)
	}
	clean := true
	if rc.def.Options.CleanSession != nil {
		clean = *rc.def.Options.CleanSession
	}
	o.SetCleanSession(clean)
	ka := 30 * time.Second
	if rc.def.Options.KeepAliveSec != nil {
		ka = time.Duration(*rc.def.Options.KeepAliveSec) * time.Second
	}
	o.SetKeepAlive(ka)
	if rc.def.Options.TLS {
		o.SetTLSConfig(&tls.Config{InsecureSkipVerify: rc.def.Options.TLSInsecure, ServerName: deref(rc.def.Options.TLSServerName)})
	}
	return o
}

func waitToken(ctx context.Context, t mqtt.Token) error {
	select {
	case <-t.Done():
		return t.Error()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// connectMQTT connects the pooled publish client (opts nil) or a session client.
func (s *Service) connectMQTT(ctx context.Context, rc *resolvedConn, suffix string, opts *mqtt.ClientOptions) (*client, error) {
	if opts == nil {
		opts = s.mqttOptions(rc, suffix)
	}
	c := mqtt.NewClient(opts)
	if err := waitToken(ctx, c.Connect()); err != nil {
		c.Disconnect(0)
		return nil, errors.New(rc.masker.Mask(fmt.Sprintf("connect %s: %v", rc.addr, err)))
	}
	return &client{kind: "mqtt", version: "MQTT 3.1.1", value: c, close: func() { c.Disconnect(250) }}, nil
}

func validTopic(t string) bool {
	return t != "" && len(t) <= 65535 && !strings.ContainsAny(t, "+#\x00")
}

func validFilter(f string) bool {
	if f == "" || len(f) > 65535 || strings.ContainsRune(f, 0) {
		return false
	}
	parts := strings.Split(f, "/")
	for i, p := range parts {
		if strings.Contains(p, "#") && (p != "#" || i != len(parts)-1) {
			return false
		}
		if strings.Contains(p, "+") && p != "+" {
			return false
		}
	}
	return true
}

func decodePayload(p, enc string) ([]byte, *Error) {
	switch enc {
	case "", "utf8":
		return []byte(p), nil
	case "json":
		if !json.Valid([]byte(p)) {
			return nil, invalid("the payload is not valid JSON")
		}
		return []byte(p), nil
	case "base64":
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
		if err != nil {
			return nil, invalid("the payload is not valid base64")
		}
		return b, nil
	case "hex":
		b, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(p), " ", ""))
		if err != nil {
			return nil, invalid("the payload is not valid hex")
		}
		return b, nil
	}
	return nil, invalid("encoding must be utf8 | json | base64 | hex")
}

func (s *Service) handleMQTTPublish(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "mqtt")
	if e != nil {
		return nil, 0, e
	}
	var req MqttPublishRequest
	if e := readBody(r, &req, 16<<20); e != nil {
		return nil, 0, e
	}
	if !validTopic(req.Topic) {
		return nil, 0, invalid("topic must be 1–65535 bytes without the wildcards + and #")
	}
	if req.QoS < 0 || req.QoS > 2 {
		return nil, 0, invalid("qos must be 0, 1 or 2")
	}
	payload, e := decodePayload(req.Payload, req.Encoding)
	if e != nil {
		return nil, 0, e
	}
	if d.ReadOnly {
		return nil, 0, forbidden("%q is read-only: publishing is refused", d.Name)
	}
	if req.Retain && len(payload) == 0 && !ConfirmOk(d.Name, req.Confirm) {
		return nil, 0, confirmRequired(d.Name, []string{"clears the retained message of " + req.Topic})
	}
	if e := s.allow("execute", c); e != nil {
		return nil, 0, e
	}
	start := time.Now()
	masker := NewMasker(s.h.SecretValues())
	log := func(ok bool, errText string) *string {
		var ev any
		if errText != "" {
			ev = LogError(errText, masker)
		}
		return s.activity(c, "devtools.mqtt-published", fmt.Sprintf("dev tools: MQTT publish %s on %q (%d bytes)", masker.Mask(req.Topic), d.Name, len(payload)),
			map[string]any{"connectionId": d.ID, "connectionName": d.Name, "topic": masker.Mask(req.Topic), "qos": req.QoS, "retain": req.Retain, "bytes": len(payload),
				"ok": ok, "error": ev, "durationMs": time.Since(start).Milliseconds()})
	}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		log(false, e.Msg)
		return nil, 0, e
	}
	masker = rc.masker
	s.noteTarget(d.ID, rc.targetText)
	ctx, done, e := s.begin(r.Context(), c, d.ID, "", s.timeoutFor(nil, rc))
	if e != nil {
		return nil, 0, e
	}
	defer done()
	_, err := s.withClient(ctx, rc, func(cl *client) error {
		mc := cl.value.(mqtt.Client)
		if !mc.IsConnectionOpen() {
			return errors.New("not connected")
		}
		return waitToken(ctx, mc.Publish(req.Topic, byte(req.QoS), req.Retain, payload))
	})
	if err != nil {
		msg := masker.Mask(err.Error())
		log(false, msg)
		return nil, 0, unavailable("%s: %s", d.Name, msg)
	}
	id := log(true, "")
	return MqttPublishResponse{OK: true, DurationMs: time.Since(start).Milliseconds(), Bytes: len(payload), ActivityID: id}, 200, nil
}

// ───────────────────────── live sessions

type mqttSession struct {
	id        string
	connID    string
	connName  string
	owner     string
	caller    Caller
	cl        mqtt.Client
	mu        sync.Mutex
	subs      []MqttSubscription
	ch        chan MqttMessage
	status    chan MqttStreamStatus
	dropped   atomic.Int64
	messages  atomic.Int64
	seq       atomic.Int64
	created   time.Time
	streaming atomic.Bool
	closed    atomic.Bool
	done      chan struct{}
	reason    string
}

// MqttStreamStatus is the data of a `status` event.
type MqttStreamStatus struct {
	Connected bool    `json:"connected"`
	Error     *string `json:"error"`
}

type mqttManager struct {
	s        *Service
	mu       sync.Mutex
	sessions map[string]*mqttSession
	topics   map[string]map[string]*MqttTopicSeen
}

func newMQTTManager(s *Service) *mqttManager {
	return &mqttManager{s: s, sessions: map[string]*mqttSession{}, topics: map[string]map[string]*MqttTopicSeen{}}
}

func (m *mqttManager) seen(connID string, msg MqttMessage, preview *string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tm := m.topics[connID]
	if tm == nil {
		tm = map[string]*MqttTopicSeen{}
		m.topics[connID] = tm
	}
	t := tm[msg.Topic]
	if t == nil {
		if len(tm) >= 1000 { // keep the last 1000 topics: drop the oldest
			var oldest string
			var at string
			for k, v := range tm {
				if oldest == "" || v.LastAt < at {
					oldest, at = k, v.LastAt
				}
			}
			delete(tm, oldest)
		}
		t = &MqttTopicSeen{Topic: msg.Topic}
		tm[msg.Topic] = t
	}
	t.Messages++
	t.LastAt = msg.At
	t.Retained = msg.Retained
	t.LastPreview = preview
	t.LastSizeBytes = msg.SizeBytes
}

func (m *mqttManager) countOwner(owner string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.owner == owner && !s.closed.Load() {
			n++
		}
	}
	return n
}

func (m *mqttManager) get(id, connID string) *mqttSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || s.connID != connID || s.closed.Load() {
		return nil
	}
	return s
}

// end closes a session once and writes devtools.mqtt-unsubscribed.
func (m *mqttManager) end(ss *mqttSession, reason string) {
	if !ss.closed.CompareAndSwap(false, true) {
		return
	}
	ss.reason = reason
	close(ss.done)
	go ss.cl.Disconnect(250)
	m.mu.Lock()
	delete(m.sessions, ss.id)
	m.mu.Unlock()
	ss.mu.Lock()
	filters := []string{}
	for _, sb := range ss.subs {
		filters = append(filters, sb.Filter)
	}
	ss.mu.Unlock()
	m.s.activity(ss.caller, "devtools.mqtt-unsubscribed", fmt.Sprintf("dev tools: MQTT session on %q ended (%s, %d messages)", ss.connName, reason, ss.messages.Load()),
		map[string]any{"connectionId": ss.connID, "connectionName": ss.connName, "sessionId": ss.id, "filters": filters, "messages": ss.messages.Load(),
			"durationSec": int64(time.Since(ss.created).Seconds()), "reason": reason})
}

func (m *mqttManager) closeConn(connID, reason string) {
	m.mu.Lock()
	var l []*mqttSession
	for _, s := range m.sessions {
		if s.connID == connID {
			l = append(l, s)
		}
	}
	delete(m.topics, connID)
	m.mu.Unlock()
	for _, s := range l {
		m.end(s, reason)
	}
}

func (m *mqttManager) closeAll(reason string) {
	m.mu.Lock()
	var l []*mqttSession
	for _, s := range m.sessions {
		l = append(l, s)
	}
	m.mu.Unlock()
	for _, s := range l {
		m.end(s, reason)
	}
}

// sweep ends sessions whose stream was never opened (2 min).
func (m *mqttManager) sweep() {
	m.mu.Lock()
	var l []*mqttSession
	for _, s := range m.sessions {
		if !s.streaming.Load() && time.Since(s.created) > 2*time.Minute {
			l = append(l, s)
		}
	}
	m.mu.Unlock()
	for _, s := range l {
		m.end(s, "the stream was not opened")
	}
}

func (s *Service) mqttMessage(ss *mqttSession, msg mqtt.Message) {
	p := msg.Payload()
	maxCell := s.limits().MaxCellBytes
	mm := MqttMessage{Seq: ss.seq.Add(1), At: ts(time.Now()), Topic: msg.Topic(), QoS: int(msg.Qos()), Retained: msg.Retained(), Duplicate: msg.Duplicate(), SizeBytes: len(p)}
	shown := p
	if len(shown) > maxCell {
		shown, mm.Truncated = shown[:maxCell], true
	}
	isText := utf8.Valid(shown)
	if !isText && mm.Truncated { // cut inside a rune: drop the partial rune at the end
		for k := 1; k <= 3 && k < len(shown); k++ {
			if utf8.Valid(shown[:len(shown)-k]) {
				shown, isText = shown[:len(shown)-k], true
				break
			}
		}
	}
	if isText {
		t := string(shown)
		mm.Text = &t
	} else {
		b := base64.StdEncoding.EncodeToString(shown)
		mm.Base64 = &b
	}
	var preview *string
	if mm.Text != nil {
		pv := TruncateUTF8(*mm.Text, 200).Text
		preview = &pv
	}
	ss.messages.Add(1)
	s.mqtt.seen(ss.connID, mm, preview)
	select {
	case ss.ch <- mm:
	default:
		ss.dropped.Add(1)
	}
}

func (s *Service) handleMQTTSession(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "mqtt")
	if e != nil {
		return nil, 0, e
	}
	var req MqttSessionRequest
	if e := readBody(r, &req, 1<<20); e != nil {
		return nil, 0, e
	}
	if len(req.Subscriptions) == 0 {
		return nil, 0, invalid("subscribe to at least one topic filter")
	}
	if len(req.Subscriptions) > 50 {
		return nil, 0, invalid("at most 50 filters per session")
	}
	for _, sb := range req.Subscriptions {
		if !validFilter(sb.Filter) || sb.QoS < 0 || sb.QoS > 2 {
			return nil, 0, invalid("%q is not a valid MQTT topic filter (qos 0–2)", sb.Filter)
		}
	}
	if e := s.allow("mqtt-session", c); e != nil {
		return nil, 0, e
	}
	if s.mqtt.countOwner(c.key()) >= 4 {
		return nil, 0, conflict("at most 4 live MQTT sessions per GUI session: close one first")
	}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		return nil, 0, e
	}
	s.noteTarget(d.ID, rc.targetText)
	ss := &mqttSession{id: "m_" + randToken(12), connID: d.ID, connName: d.Name, owner: c.key(), caller: c, subs: append([]MqttSubscription(nil), req.Subscriptions...),
		ch: make(chan MqttMessage, 1000), status: make(chan MqttStreamStatus, 16), created: time.Now(), done: make(chan struct{})}
	opts := s.mqttOptions(rc, "live")
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) { s.mqttMessage(ss, msg) })
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		m := rc.masker.Mask(err.Error())
		select {
		case ss.status <- MqttStreamStatus{Connected: false, Error: &m}:
		default:
		}
	})
	opts.SetAutoReconnect(true).SetMaxReconnectInterval(10 * time.Second)
	opts.SetOnConnectHandler(func(cl mqtt.Client) {
		ss.mu.Lock()
		subs := append([]MqttSubscription(nil), ss.subs...)
		ss.mu.Unlock()
		for _, sb := range subs {
			cl.Subscribe(sb.Filter, byte(sb.QoS), nil)
		}
		select {
		case ss.status <- MqttStreamStatus{Connected: true}:
		default:
		}
	})
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cl, err := s.connectMQTT(ctx, rc, "", opts)
	if err != nil {
		return nil, 0, unavailable("%s: %s", d.Name, err.Error())
	}
	ss.cl = cl.value.(mqtt.Client)
	s.mqtt.mu.Lock()
	s.mqtt.sessions[ss.id] = ss
	s.mqtt.mu.Unlock()
	filters := []string{}
	for _, sb := range ss.subs {
		filters = append(filters, sb.Filter)
	}
	s.activity(c, "devtools.mqtt-subscribed", fmt.Sprintf("dev tools: MQTT session on %q (%s)", d.Name, strings.Join(filters, ", ")),
		map[string]any{"connectionId": d.ID, "connectionName": d.Name, "sessionId": ss.id, "filters": filters})
	tk, exp := s.tickets.issue(&ticket{kind: "mqtt", ref: ss.id, sessionID: c.SessionID, via: c.Via, actor: c.Actor}, 60*time.Second)
	path := fmt.Sprintf("/_opm/api/sites/%s/devtools/connections/%s/mqtt/sessions/%s/stream?ticket=%s", s.h.Site(), d.ID, ss.id, tk)
	return MqttSessionResponse{SessionID: ss.id, Ticket: tk, StreamPath: path, ExpiresAt: ts(exp)}, 201, nil
}

func sseEvent(w http.ResponseWriter, rc *http.ResponseController, event string, data any) error {
	b, _ := json.Marshal(data)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return rc.Flush()
}

func (s *Service) handleMQTTStream(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	connID, sid := r.PathValue("connectionId"), r.PathValue("sessionId")
	if e := s.tickets.take(r.URL.Query().Get("ticket"), "mqtt", sid, c); e != nil {
		return nil, 0, e
	}
	ss := s.mqtt.get(sid, connID)
	if ss == nil {
		return nil, 0, notFound("MQTT session %s is gone", sid)
	}
	if !ss.streaming.CompareAndSwap(false, true) {
		return nil, 0, conflict("the session is already streamed")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	rcw := http.NewResponseController(w)
	ss.mu.Lock()
	meta := map[string]any{"sessionId": ss.id, "subscriptions": append([]MqttSubscription(nil), ss.subs...)}
	ss.mu.Unlock()
	_ = sseEvent(w, rcw, "meta", meta)
	_ = sseEvent(w, rcw, "status", MqttStreamStatus{Connected: ss.cl.IsConnectionOpen()})
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	maxDur := time.NewTimer(time.Duration(s.limits().MqttStreamMaxSec) * time.Second)
	defer maxDur.Stop()
	reason := "stream closed"
	flushDropped := func() error {
		if n := ss.dropped.Swap(0); n > 0 {
			return sseEvent(w, rcw, "dropped", map[string]int64{"count": n})
		}
		return nil
	}
loop:
	for {
		select {
		case <-r.Context().Done():
			break loop
		case <-ss.done:
			_ = sseEvent(w, rcw, "end", map[string]string{"reason": ss.reason})
			return nil, 0, nil
		case <-maxDur.C:
			reason = "the stream reached its maximum duration"
			_ = sseEvent(w, rcw, "end", map[string]string{"reason": reason})
			break loop
		case st := <-ss.status:
			if sseEvent(w, rcw, "status", st) != nil {
				break loop
			}
		case m := <-ss.ch:
			if flushDropped() != nil || sseEvent(w, rcw, "message", m) != nil {
				break loop
			}
		case <-ping.C:
			if flushDropped() != nil {
				break loop
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rcw.Flush() != nil {
				break loop
			}
		}
	}
	s.mqtt.end(ss, reason)
	return nil, 0, nil
}

func (s *Service) ownSession(r *http.Request, c Caller) (*mqttSession, *Error) {
	ss := s.mqtt.get(r.PathValue("sessionId"), r.PathValue("connectionId"))
	if ss == nil {
		return nil, notFound("MQTT session %s is gone", r.PathValue("sessionId"))
	}
	if ss.owner != c.key() {
		return nil, notFound("MQTT session %s belongs to another GUI session", ss.id)
	}
	return ss, nil
}

func (s *Service) handleMQTTSubscribe(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	ss, e := s.ownSession(r, c)
	if e != nil {
		return nil, 0, e
	}
	var sb MqttSubscription
	if e := readBody(r, &sb, 64<<10); e != nil {
		return nil, 0, e
	}
	if !validFilter(sb.Filter) || sb.QoS < 0 || sb.QoS > 2 {
		return nil, 0, invalid("%q is not a valid MQTT topic filter (qos 0–2)", sb.Filter)
	}
	ss.mu.Lock()
	if len(ss.subs) >= 50 {
		ss.mu.Unlock()
		return nil, 0, invalid("at most 50 filters per session")
	}
	ss.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := waitToken(ctx, ss.cl.Subscribe(sb.Filter, byte(sb.QoS), nil)); err != nil {
		return nil, 0, unavailable("subscribe: %s", err.Error())
	}
	ss.mu.Lock()
	replaced := false
	for i := range ss.subs {
		if ss.subs[i].Filter == sb.Filter {
			ss.subs[i].QoS, replaced = sb.QoS, true
		}
	}
	if !replaced {
		ss.subs = append(ss.subs, sb)
	}
	subs := append([]MqttSubscription(nil), ss.subs...)
	ss.mu.Unlock()
	s.activity(c, "devtools.mqtt-subscribed", fmt.Sprintf("dev tools: MQTT session on %q + %s", ss.connName, sb.Filter),
		map[string]any{"connectionId": ss.connID, "connectionName": ss.connName, "sessionId": ss.id, "filters": []string{sb.Filter}})
	return map[string]any{"subscriptions": subs}, 200, nil
}

func (s *Service) handleMQTTUnsubscribe(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	ss, e := s.ownSession(r, c)
	if e != nil {
		return nil, 0, e
	}
	var req struct {
		Filter string `json:"filter"`
	}
	if e := readBody(r, &req, 64<<10); e != nil {
		return nil, 0, e
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_ = waitToken(ctx, ss.cl.Unsubscribe(req.Filter))
	ss.mu.Lock()
	var keep []MqttSubscription
	for _, sb := range ss.subs {
		if sb.Filter != req.Filter {
			keep = append(keep, sb)
		}
	}
	if keep == nil {
		keep = []MqttSubscription{}
	}
	ss.subs = keep
	subs := append([]MqttSubscription{}, keep...)
	ss.mu.Unlock()
	return map[string]any{"subscriptions": subs}, 200, nil
}

func (s *Service) handleMQTTClose(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	ss, e := s.ownSession(r, c)
	if e != nil {
		return nil, 0, e
	}
	s.mqtt.end(ss, "closed")
	return map[string]bool{"closed": true}, 200, nil
}

func (s *Service) handleMQTTTopics(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "mqtt")
	if e != nil {
		return nil, 0, e
	}
	s.mqtt.mu.Lock()
	items := []MqttTopicSeen{}
	for _, t := range s.mqtt.topics[d.ID] {
		items = append(items, *t)
	}
	s.mqtt.mu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].Topic < items[j].Topic })
	return model.ListResponse[MqttTopicSeen]{Items: items}, 200, nil
}
