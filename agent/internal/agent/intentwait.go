package agent

// §21 #95 hold-to-fast-forward of an intent wait (approve, rollback to a snapshot, reset a volume to its starter
// content). Mirror of shared/src/intent-wait.ts (same rules, same test cases in intentwait_test.go).
//
// The wait counts EFFECTIVE seconds = Σ speed × real dt, integrated on the agent's OWN clock (a.clock). A speed > 1
// applies from the moment the agent received it until the next speed message, or until IntentSpeedDecayMs after it
// (the heartbeat deadline) — then it decays to ×1. ACCEPT needs effective ≥ waitSec, so the minimum real wait is
// waitSec / IntentMaxSpeed. The state lives in memory (monotonic times) and is written through to SQLite
// (intent_waits) so a restart keeps the progress.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"opm/agent/internal/model"
)

type intentWait struct {
	WaitSec   float64    `json:"waitSec"`
	Created   time.Time  `json:"created"`
	Effective float64    `json:"effective"` // up to Last
	Last      time.Time  `json:"last"`
	Speed     int        `json:"speed"`
	Until     time.Time  `json:"until"` // speed > 1 valid until (heartbeat deadline)
	MaxSpeed  int        `json:"maxSpeed"`
	ReadyAt   *time.Time `json:"readyAt,omitempty"` // real time effective first reached WaitSec
}

var errIntentSpeed = errors.New("speed must be 1, 2 or 4")

func newIntentWait(wait time.Duration, now time.Time) *intentWait {
	return &intentWait{WaitSec: wait.Seconds(), Created: now, Last: now, Speed: 1, Until: now, MaxSpeed: 1}
}

func (s *intentWait) credit(to time.Time, speed int) {
	if !to.After(s.Last) {
		return
	}
	before := s.Effective
	s.Effective += float64(speed) * to.Sub(s.Last).Seconds()
	if s.ReadyAt == nil && s.Effective >= s.WaitSec-1e-9 {
		at := s.Last.Add(time.Duration((s.WaitSec - before) / float64(speed) * float64(time.Second)))
		s.ReadyAt = &at
	}
	s.Last = to
}

// advance integrates up to now; a clock that went backwards credits nothing.
func (s *intentWait) advance(now time.Time) {
	if !now.After(s.Last) {
		return
	}
	if s.Speed > 1 {
		end := now
		if s.Until.Before(end) {
			end = s.Until
		}
		s.credit(end, s.Speed)
		if !now.Before(s.Until) {
			s.Speed = 1
		}
	}
	if s.Speed <= 1 {
		s.Speed = 1
		s.credit(now, 1)
	}
}

// setSpeed is a speed message (a change or a heartbeat) received at now.
func (s *intentWait) setSpeed(speed float64, now time.Time) error {
	if speed != 1 && speed != 2 && speed != 4 {
		return errIntentSpeed
	}
	s.advance(now)
	s.Speed = int(speed)
	s.Until = now
	if s.Speed > 1 {
		s.Until = now.Add(model.IntentSpeedDecayMs * time.Millisecond)
	}
	if s.Speed > s.MaxSpeed {
		s.MaxSpeed = s.Speed
	}
	return nil
}

func (s *intentWait) ready() bool { return s.Effective >= s.WaitSec-1e-9 }

func (s *intentWait) remaining() float64 { return math.Max(0, s.WaitSec-s.Effective) }

// fastForward is the activity / run record; nil when the wait was never fast-forwarded.
func (s *intentWait) fastForward(usedBy string) *model.FastForwardUse {
	if s.MaxSpeed <= 1 {
		return nil
	}
	end := s.Last
	if s.ReadyAt != nil {
		end = *s.ReadyAt
	}
	return &model.FastForwardUse{UsedBy: usedBy, MaxSpeed: s.MaxSpeed, RealSeconds: math.Round(end.Sub(s.Created).Seconds()*10) / 10, EffectiveSeconds: s.WaitSec}
}

func fastForwardLabel(ff *model.FastForwardUse) string {
	return fmt.Sprintf("fast-forwarded (×%d, %d s real)", ff.MaxSpeed, int(math.Round(ff.RealSeconds)))
}

// ───────────────────────── storage (memory + intent_waits)

// loadIntentWait returns the wait of an intent (caller holds a.iwMu). Missing state (an intent from before §21 #95)
// is rebuilt from notBefore at ×1: created = notBefore − ApproveWait.
func (a *Agent) loadIntentWait(id string, notBefore int64) *intentWait {
	if s, ok := a.iw[id]; ok {
		return s
	}
	var raw string
	if err := a.db.QueryRow(`SELECT state FROM intent_waits WHERE intentId = ?`, id).Scan(&raw); err == nil {
		var s intentWait
		if json.Unmarshal([]byte(raw), &s) == nil && s.WaitSec > 0 {
			a.iw[id] = &s
			return &s
		}
	}
	s := newIntentWait(a.opt.ApproveWait, time.Unix(notBefore, 0).Add(-a.opt.ApproveWait))
	a.iw[id] = s
	return s
}

func (a *Agent) saveIntentWait(id string, s *intentWait) {
	b, _ := json.Marshal(s)
	_, _ = a.db.Exec(`INSERT INTO intent_waits(intentId, state) VALUES(?, ?) ON CONFLICT(intentId) DO UPDATE SET state = excluded.state`, id, string(b))
}

func (a *Agent) dropIntentWait(id string) {
	a.iwMu.Lock()
	delete(a.iw, id)
	a.iwMu.Unlock()
	_, _ = a.db.Exec(`DELETE FROM intent_waits WHERE intentId = ?`, id)
}

func (a *Agent) intentProgress(id, kind string, s *intentWait, now time.Time) model.IntentProgress {
	p := model.IntentProgress{IntentID: id, Kind: kind, WaitSec: s.WaitSec, EffectiveSec: math.Round(s.Effective*1000) / 1000,
		RemainingSec: math.Round(s.remaining()*1000) / 1000, Speed: s.Speed, MaxSpeed: s.MaxSpeed, Ready: s.ready(),
		CreatedAt: tsOf(s.Created), ServerTime: tsOf(now)}
	if s.Speed > 1 {
		u := tsOf(s.Until)
		p.SpeedUntil = &u
	}
	return p
}

// pendingIntent reads an unused, unexpired intent of THIS session (else not-found).
func (a *Agent) pendingIntent(id string, rc *reqCtx, now time.Time) (kind string, notBefore int64, e *httpError) {
	var sid string
	var exp int64
	var used sql.NullInt64
	err := a.db.QueryRow(`SELECT kind, sessionId, notBefore, expiresAt, usedAt FROM intents WHERE intentId = ?`, id).Scan(&kind, &sid, &notBefore, &exp, &used)
	if err != nil || sid != rc.sessionID || used.Valid {
		return "", 0, herr(model.ErrNotFound, "no such pending intent", nil)
	}
	if now.Unix() >= exp {
		return "", 0, herr(model.ErrNotFound, "the intent expired; start again", nil)
	}
	return kind, notBefore, nil
}

// handleGetIntent: GET /_opm/api/intents/{intentId} (+ per-site /intents/{intentId}).
func (a *Agent) handleGetIntent(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("intentId")
	now := a.clock()
	kind, nb, e := a.pendingIntent(id, getRC(r), now)
	if e != nil {
		return nil, 0, e
	}
	a.iwMu.Lock()
	defer a.iwMu.Unlock()
	s := a.loadIntentWait(id, nb)
	s.advance(now)
	return a.intentProgress(id, kind, s, now), 200, nil
}

// handleIntentSpeed: POST /_opm/api/intents/{intentId}/speed {speed} (+ per-site).
func (a *Agent) handleIntentSpeed(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.IntentSpeedRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	id := r.PathValue("intentId")
	now := a.clock()
	kind, nb, e := a.pendingIntent(id, getRC(r), now)
	if e != nil {
		return nil, 0, e
	}
	a.iwMu.Lock()
	defer a.iwMu.Unlock()
	s := a.loadIntentWait(id, nb)
	if err := s.setSpeed(req.Speed, now); err != nil {
		return nil, 0, invalid(err.Error(), []model.ValidationIssue{{Path: "speed", Code: "V_RANGE", Message: "1, 2 or 4 (at most ×4)", Severity: "error"}})
	}
	a.saveIntentWait(id, s)
	return a.intentProgress(id, kind, s, now), 200, nil
}
