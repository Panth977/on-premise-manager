package driver

import (
	"testing"
	"time"

	"opm/agent/internal/model"
)

func TestCompleteLimit(t *testing.T) {
	base := 300 * time.Second
	cases := []struct {
		name string
		h    *model.HealthCheck
		want time.Duration
	}{
		{"no check", nil, base},
		{"type none", &model.HealthCheck{Type: "none", StartPeriodSec: 900}, base},
		{"short start period keeps the agent limit", &model.HealthCheck{Type: "http", StartPeriodSec: 30, IntervalSec: 10, TimeoutSec: 5, Retries: 3}, base},
		{"long start period extends it", &model.HealthCheck{Type: "exec", StartPeriodSec: 900, IntervalSec: 30, TimeoutSec: 5, Retries: 4}, 900*time.Second + 4*35*time.Second},
		{"retries default 3", &model.HealthCheck{Type: "tcp", StartPeriodSec: 600, IntervalSec: 5, TimeoutSec: 5}, 600*time.Second + 3*10*time.Second},
	}
	for _, c := range cases {
		if got := CompleteLimit(base, c.h); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
