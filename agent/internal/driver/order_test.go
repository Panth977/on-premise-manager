package driver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// RunStarts (§21 #93): a container waits only for its depends; every other readiness wait runs in parallel.
func TestRunStartsParallelAndWaits(t *testing.T) {
	var mu sync.Mutex
	started := map[string]time.Time{}
	readyAt := map[string]time.Time{}
	mark := func(m map[string]time.Time, s string) { mu.Lock(); m[s] = time.Now(); mu.Unlock() }
	has := func(s string) bool { mu.Lock(); defer mu.Unlock(); _, ok := started[s]; return ok }
	step := func(slug string, wait []string, ready func(ctx context.Context) error) StartStep {
		return StartStep{Slug: slug, Wait: wait,
			Start: func(context.Context) (string, error) { mark(started, slug); return "id-" + slug, nil },
			Ready: func(ctx context.Context, id string) error {
				if err := ready(ctx); err != nil {
					return err
				}
				mark(readyAt, slug)
				return nil
			}}
	}
	// the user's case: "backend" (declared first, no depends) is only ready once "mqtt" exists. The old serial apply
	// waited for backend before creating mqtt → 300 s timeout. Now both start and backend becomes ready.
	backend := step("backend", nil, func(ctx context.Context) error {
		for !has("mqtt") {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
		return nil
	})
	mqtt := step("mqtt", nil, func(context.Context) error { time.Sleep(30 * time.Millisecond); return nil })
	worker := step("worker", []string{"mqtt"}, func(context.Context) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := RunStarts(ctx, []StartStep{backend, mqtt, worker}, true, nil)
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Slug, r.Err)
		}
	}
	if !started["worker"].After(readyAt["mqtt"]) && !started["worker"].Equal(readyAt["mqtt"]) {
		t.Fatalf("worker must start after mqtt is ready: started %v, mqtt ready %v", started["worker"], readyAt["mqtt"])
	}
}

func TestRunStartsFailFastAndRollback(t *testing.T) {
	boom := errors.New("boom")
	mk := func(slug string, wait []string, err error, d time.Duration) StartStep {
		return StartStep{Slug: slug, Wait: wait, Start: func(context.Context) (string, error) { return slug, nil },
			Ready: func(ctx context.Context, _ string) error {
				select {
				case <-time.After(d):
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
	}
	steps := []StartStep{mk("db", nil, boom, 20*time.Millisecond), mk("slow", nil, nil, 2*time.Second), mk("api", []string{"db"}, nil, 0)}
	var got []StartResult
	res := RunStarts(context.Background(), steps, true, func(r StartResult) { got = append(got, r) })
	if len(got) != 3 {
		t.Fatalf("one result per step: %+v", got)
	}
	by := map[string]StartResult{}
	for _, r := range res {
		by[r.Slug] = r
	}
	if !errors.Is(by["db"].Err, boom) || by["db"].Phase != "ready" {
		t.Fatalf("db %+v", by["db"])
	}
	if by["slow"].Phase != "cancelled" {
		t.Fatalf("slow must be cancelled after the first failure: %+v", by["slow"])
	}
	if by["api"].Phase != "cancelled" && by["api"].Phase != "dependency" {
		t.Fatalf("api must not start after its dependency failed: %+v", by["api"])
	}
	// rollback (best effort): a failed dependency does not block
	res = RunStarts(context.Background(), []StartStep{mk("db", nil, boom, 0), mk("api", []string{"db"}, nil, 0)}, false, nil)
	if res[1].Err != nil || res[1].Phase != "ready" {
		t.Fatalf("rollback must still start api: %+v", res[1])
	}
	// a Wait on a LATER step (reference cycle) is ignored: no deadlock
	done := make(chan struct{})
	go func() {
		RunStarts(context.Background(), []StartStep{mk("a", []string{"b"}, nil, 0), mk("b", []string{"a"}, nil, 0)}, true, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock on a cycle")
	}
	// a start error stops launching
	st := []StartStep{{Slug: "x", Start: func(context.Context) (string, error) { return "", boom }, Ready: func(context.Context, string) error { return nil }}, mk("y", nil, nil, 0)}
	res = RunStarts(context.Background(), st, true, nil)
	if res[0].Phase != "start" || res[1].Phase != "cancelled" {
		t.Fatalf("start failure: %+v", res)
	}
	// keep steps: Ready only
	called := false
	res = RunStarts(context.Background(), []StartStep{{Slug: "k", ID: "kid", Ready: func(_ context.Context, id string) error { called = id == "kid"; return nil }}}, true, nil)
	if !called || res[0].ID != "kid" {
		t.Fatalf("keep step: %+v", res)
	}
}
