package ulid

import (
	"regexp"
	"testing"
	"time"
)

func TestULID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	prev := ""
	for i := 0; i < 2000; i++ {
		id := New()
		if !re.MatchString(id) {
			t.Fatalf("bad ulid %q", id)
		}
		if id <= prev {
			t.Fatalf("not monotonic: %s <= %s", id, prev)
		}
		prev = id
	}
	now := time.Now()
	id := At(now.Add(time.Hour))
	if got := Time(id); got.UnixMilli() != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("time %v", got)
	}
}
