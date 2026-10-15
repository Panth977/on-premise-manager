package model

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestActivityTypesMirror: ActivityTypes equals TS ACTIVITY_TYPES (shared/src/types.ts), in order (round G adds
// access-key.revealed, secret.regenerated, install-token.revealed). REVEAL_* constants match too.
func TestActivityTypesMirror(t *testing.T) {
	b, err := os.ReadFile("../../../shared/src/types.ts")
	if err != nil {
		t.Skip("shared/src/types.ts not found: ", err)
	}
	src := string(b)
	i := strings.Index(src, "export const ACTIVITY_TYPES = [")
	if i < 0 {
		t.Fatal("ACTIVITY_TYPES not found")
	}
	block := src[i+len("export const ACTIVITY_TYPES = ["):]
	block = block[:strings.Index(block, "] as const")]
	var ts []string
	for _, line := range strings.Split(block, "\n") { // comments may contain quotes / brackets (§21 #90)
		if j := strings.Index(line, "//"); j >= 0 {
			line = line[:j]
		}
		for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(line, -1) {
			ts = append(ts, m[1])
		}
	}
	if !reflect.DeepEqual(ts, ActivityTypes) {
		t.Fatalf("ActivityTypes differ from TS\nTS: %v\nGo: %v", ts, ActivityTypes)
	}
	for _, want := range []string{"access-key.revealed", "secret.regenerated", "install-token.revealed"} {
		if !strings.Contains(strings.Join(ActivityTypes, ","), want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(src, "export const REVEAL_SECONDS = 60 as const") || RevealSeconds != 60 {
		t.Error("REVEAL_SECONDS mirror")
	}
	if !strings.Contains(src, "export const REVEAL_RATE_PER_MINUTE = 30 as const") || RevealRatePerMinute != 30 {
		t.Error("REVEAL_RATE_PER_MINUTE mirror")
	}
}
