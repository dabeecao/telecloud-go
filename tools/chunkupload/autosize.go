package main

import (
	"bytes"
	"fmt"
	"io"
	"time"
)

const mib = int64(1 << 20)

// chooseChunk picks a chunk size so one chunk request takes about
// targetSec seconds when `parallel` chunks share the measured uplink, which
// keeps it under the ~100 s origin timeout of proxies such as Cloudflare
// Tunnel. The result is rounded down to whole MiB within [min, ceiling]. An
// unusable measurement falls back to a conservative 16 MiB.
func chooseChunk(bytesPerSec float64, parallel int, minB, ceiling int64, targetSec int) int64 {
	if parallel < 1 {
		parallel = 1
	}
	if !(bytesPerSec > 0) || bytesPerSec > 1e12 {
		return clampMiB(16*mib, minB, ceiling)
	}
	return clampMiB(int64(bytesPerSec*float64(targetSec)/float64(parallel)), minB, ceiling)
}

func clampMiB(v, minB, ceiling int64) int64 {
	v = v / mib * mib
	if v > ceiling {
		v = ceiling / mib * mib
	}
	if v < minB {
		v = minB
	}
	return v
}

// probeSpeed times a 4 MiB upload to /api/uploads/probe (after a small warm-up
// so TLS/HTTP2 setup is not counted) and returns bytes per second.
func (c *client) probeSpeed() (float64, error) {
	send := func(n int64) (time.Duration, error) {
		t := time.Now()
		resp, err := c.do("POST", "/api/uploads/probe", bytes.NewReader(make([]byte, n)), n, nil)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			return 0, fmt.Errorf("probe: HTTP %d", resp.StatusCode)
		}
		return time.Since(t), nil
	}
	if _, err := send(256 << 10); err != nil {
		return 0, err
	}
	d, err := send(4 * mib)
	if err != nil {
		return 0, err
	}
	return float64(4*mib) / d.Seconds(), nil
}

type upConfig struct {
	ChunkSizeB int64 `json:"chunk_size_bytes"`
	MinChunkB  int64 `json:"min_chunk_size_bytes"`
	TargetSec  int   `json:"target_chunk_seconds"`
}

// autoChunk returns the chunk size for a new session: measured speed, capped
// by the server's configured size.
func (c *client) autoChunk(parallel int) int64 {
	var cfg upConfig
	if _, err := c.json("GET", "/api/uploads/config", nil, &cfg); err != nil || cfg.ChunkSizeB == 0 {
		return 16 * mib
	}
	// Below ~4 MiB the per-request overhead dominates, so use that as the floor.
	if cfg.MinChunkB < 4*mib {
		cfg.MinChunkB = 4 * mib
	}
	if cfg.TargetSec == 0 {
		cfg.TargetSec = 40
	}
	bps, err := c.probeSpeed()
	if err != nil {
		fmt.Printf("speed probe failed (%v); using a conservative chunk size\n", err)
		bps = 0
	} else {
		fmt.Printf("measured upload speed %.1f MiB/s\n", bps/float64(mib))
	}
	return chooseChunk(bps, parallel, cfg.MinChunkB, cfg.ChunkSizeB, cfg.TargetSec)
}
