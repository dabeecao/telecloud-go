package tgclient

import (
	"errors"
	"testing"
	"time"
)

func TestRetryDelayHonoursFloodWait(t *testing.T) {
	err := errors.New("rpc error code 420: FLOOD_WAIT_45")
	for i := 0; i < 50; i++ {
		d, flood := retryDelay(1, err)
		if !flood {
			t.Fatal("flood wait not detected")
		}
		if d < 45*time.Second || d > 48*time.Second {
			t.Fatalf("flood wait must be honoured (45s + <3s jitter), got %s", d)
		}
	}
}

func TestRetryDelayExponentialWithJitterAndCap(t *testing.T) {
	err := errors.New("connection reset")
	seen := map[time.Duration]bool{}
	for attempt := 1; attempt <= 12; attempt++ {
		ceil := partRetryBase << uint(attempt-1)
		if ceil > partRetryMax {
			ceil = partRetryMax
		}
		for i := 0; i < 50; i++ {
			d, flood := retryDelay(attempt, err)
			if flood {
				t.Fatal("not a flood wait")
			}
			if d < ceil/2 || d > ceil {
				t.Fatalf("attempt %d: delay %s outside [%s,%s]", attempt, d, ceil/2, ceil)
			}
			seen[d] = true
		}
	}
	if len(seen) < 20 {
		t.Fatalf("delays are not jittered (%d distinct values)", len(seen))
	}
}
