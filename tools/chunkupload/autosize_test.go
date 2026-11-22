package main

import "testing"

func TestChooseChunk(t *testing.T) {
	cases := []struct {
		name     string
		bps      float64
		parallel int
		want     int64
	}{
		{"slow link gets the minimum", 0.2 * float64(mib), 3, 4 * mib},
		{"3 MB/s, 3 streams", 3 * float64(mib), 3, 40 * mib},
		{"3 MB/s, 1 stream hits the ceiling", 3 * float64(mib), 1, 50 * mib},
		{"fast link hits the ceiling", 100 * float64(mib), 4, 50 * mib},
		{"rounds down to whole MiB", 1.27 * float64(mib), 2, 25 * mib},
		{"unusable measurement falls back", 0, 3, 16 * mib},
		{"NaN-like input falls back", -1, 3, 16 * mib},
	}
	for _, c := range cases {
		got := chooseChunk(c.bps, c.parallel, 4*mib, 50*mib, 40)
		if got != c.want {
			t.Errorf("%s: got %d MiB, want %d MiB", c.name, got/mib, c.want/mib)
		}
		if got%mib != 0 || got < 4*mib || got > 50*mib {
			t.Errorf("%s: %d is not a whole MiB within [4,50]", c.name, got)
		}
	}
	if got := chooseChunk(1e9, 4, 4*mib, 32*mib, 40); got != 32*mib {
		t.Errorf("the server ceiling must win, got %d MiB", got/mib)
	}
}
