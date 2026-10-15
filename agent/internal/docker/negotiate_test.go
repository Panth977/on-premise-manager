package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon answers GET /version (unversioned) with apiVersion and records every other path.
func fakeDaemon(t *testing.T, apiVersion, minAPI string) (*Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "x", "ApiVersion": apiVersion, "MinAPIVersion": minAPI})
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/networks/n1"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"network n1 not found"}`))
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["CheckDuplicate"]; ok {
				mu.Lock()
				paths = append(paths, "CheckDuplicate")
				mu.Unlock()
			}
			_, _ = w.Write([]byte(`{"Id":"x"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return New("tcp://" + strings.TrimPrefix(srv.URL, "http://")), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func TestNegotiateAPIVersion(t *testing.T) {
	ctx := context.Background()
	cases := []struct{ server, min, want string }{
		{"1.41", "1.12", "1.41"}, // Docker 20.10: the floor
		{"1.43", "1.12", "1.43"}, // Docker 24.0
		{"1.45", "1.24", "1.45"}, // between: the server's max
		{"1.47", "1.24", "1.47"},
		{"1.51", "1.24", "1.47"}, // Docker 28: capped
		{"1.52", "1.44", "1.47"}, // Docker 29 (min 1.44): capped, above its min
		{"1.60", "1.50", "1.50"}, // hypothetical future daemon that dropped 1.47: its own minimum
	}
	for _, c := range cases {
		cl, paths := fakeDaemon(t, c.server, c.min)
		if err := cl.Ping(ctx); err != nil {
			t.Fatalf("%s: ping %v", c.server, err)
		}
		if err := cl.Ping(ctx); err != nil { // negotiated once
			t.Fatal(err)
		}
		if got := cl.APIVersion(); got != c.want {
			t.Errorf("server %s → %s, want %s", c.server, got, c.want)
		}
		p := paths()
		if len(p) != 3 || p[0] != "GET /version" || p[1] != "GET /v"+c.want+"/_ping" || p[2] != p[1] {
			t.Errorf("server %s: paths %v", c.server, p)
		}
	}
}

func TestNegotiateRefusesOldDaemon(t *testing.T) {
	for _, old := range []string{"1.40", "1.39", ""} {
		cl, paths := fakeDaemon(t, old, "1.12")
		err := cl.Ping(context.Background())
		if !errors.Is(err, ErrAPITooOld) || !strings.Contains(err.Error(), "Docker 20.10 / API 1.41 or newer") {
			t.Fatalf("server %q: %v", old, err)
		}
		if cl.APIVersion() != "" {
			t.Fatal("refusal must not be cached as a version")
		}
		_ = cl.Ping(context.Background()) // tries again (not cached)
		for _, p := range paths() {
			if p != "GET /version" {
				t.Fatalf("server %q: a versioned request was sent: %v", old, paths())
			}
		}
		if len(paths()) != 2 {
			t.Fatalf("server %q: paths %v", old, paths())
		}
	}
}

func TestCheckDuplicateOnlyBefore144(t *testing.T) {
	for _, c := range []struct {
		server string
		want   bool
	}{{"1.41", true}, {"1.43", true}, {"1.44", false}, {"1.47", false}} {
		cl, paths := fakeDaemon(t, c.server, "1.12")
		if err := cl.EnsureNetwork(context.Background(), "n1", nil); err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(strings.Join(paths(), ","), "CheckDuplicate")
		if got != c.want {
			t.Errorf("server %s: CheckDuplicate sent %v", c.server, got)
		}
	}
}

func TestCmpAPI(t *testing.T) {
	if cmpAPI("1.9", "1.43") >= 0 || cmpAPI("1.43", "1.43") != 0 || cmpAPI("1.100", "1.47") <= 0 || cmpAPI("2.0", "1.99") <= 0 {
		t.Fatal("cmpAPI")
	}
}
