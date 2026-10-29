// Command chunkupload is a reference client for TeleCloud's resumable upload
// sessions (POST /api/uploads ...). It streams the file in fixed-size chunks
// with bounded memory, resumes interrupted uploads, retries with exponential
// backoff and jitter, and verifies the result against the source checksum.
//
// Credentials are read from TC_USER / TC_PASS (never from arguments).
//
//	TC_USER=me TC_PASS=... chunkupload -url https://cloud.example.com -file big.iso -path /Backups
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// waitBudget bounds how long a chunk may be deferred by server backpressure.
const waitBudget = 45 * time.Minute

type session struct {
	ID              string     `json:"id"`
	State           string     `json:"state"`
	TotalSize       int64      `json:"total_size"`
	ChunkSize       int64      `json:"chunk_size"`
	TotalChunks     int64      `json:"total_chunks"`
	Filename        string     `json:"filename"`
	Path            string     `json:"path"`
	ExpectedSHA256  string     `json:"expected_sha256"`
	BytesReceived   int64      `json:"bytes_received"`
	ConfirmedChunks int64      `json:"confirmed_chunks"`
	ConfirmedRanges [][2]int64 `json:"confirmed_ranges"`
	RetryCount      int64      `json:"retry_count"`
	FailureReason   string     `json:"failure_reason"`
	FileID          *int64     `json:"file_id"`
	FinalName       string     `json:"final_name"`
	PartsStored     int64      `json:"parts_stored"`
	TotalParts      int64      `json:"total_parts"`
	Throughput      float64    `json:"throughput_bytes_per_sec"`
}

type client struct {
	base, user, pass string
	http             *http.Client
}

func (c *client) do(method, path string, body io.Reader, size int64, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	if size >= 0 {
		req.ContentLength = size
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.http.Do(req)
}

func (c *client) json(method, path string, in, out any) (int, error) {
	var body io.Reader
	var n int64 = -1
	hdr := map[string]string{}
	if in != nil {
		b, _ := json.Marshal(in)
		body, n = bytes.NewReader(b), int64(len(b))
		hdr["Content-Type"] = "application/json"
	}
	resp, err := c.do(method, path, body, n, hdr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	} else if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(raw))
	}
	return resp.StatusCode, nil
}

func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter + time.Duration(rand.Int63n(int64(500*time.Millisecond)))
	}
	d := time.Second << uint(attempt)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func fileSHA(f *os.File, off, n int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, off, n)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func main() {
	var (
		baseURL   = flag.String("url", "", "TeleCloud base URL")
		file      = flag.String("file", "", "file to upload")
		dest      = flag.String("path", "/", "destination folder")
		chunkMB   = flag.Int64("chunk-mb", 0, "chunk size in MiB (1..50); 0 = measure the uplink and choose automatically")
		parallel  = flag.Int("parallel", 3, "parallel chunk uploads")
		resume    = flag.String("resume", "", "session ID to resume (default: auto-detect)")
		overwrite = flag.Bool("overwrite", false, "replace an existing file of the same name")
		verify    = flag.Bool("verify-download", false, "download the published file and compare its SHA-256")
		killAfter = flag.Int("test-die-after", 0, "TEST: exit abruptly after N chunks were confirmed")
		corrupt   = flag.Int64("test-corrupt-chunk", -1, "TEST: send this chunk with a corrupted body once")
		dupes     = flag.Bool("test-duplicates", false, "TEST: send every chunk twice")
		retries   = flag.Int("retries", 8, "attempts per chunk")
	)
	flag.Parse()
	if *baseURL == "" || *file == "" {
		flag.Usage()
		os.Exit(2)
	}
	c := &client{base: *baseURL, user: os.Getenv("TC_USER"), pass: os.Getenv("TC_PASS"),
		// ExpectContinueTimeout lets the server reject a chunk (e.g. 429
		// backpressure) before the body is sent; the timeout makes proxies that
		// do not support Expect fall back to sending immediately.
		http: &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ExpectContinueTimeout: 2 * time.Second,
			MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second, ForceAttemptHTTP2: true}}}

	f, err := os.Open(*file)
	check(err)
	st, err := f.Stat()
	check(err)
	size := st.Size()

	start := time.Now()
	fmt.Printf("hashing %s (%d bytes)...\n", *file, size)
	whole, err := fileSHA(f, 0, size)
	check(err)
	fmt.Printf("source sha256 %s (%.1fs)\n", whole, time.Since(start).Seconds())

	// Find or create the session.
	var s session
	if *resume != "" {
		_, err = c.json("GET", "/api/uploads/"+*resume, nil, &s)
		check(err)
	} else {
		// Creating is idempotent: an identical open session (same file,
		// destination and checksum) is returned instead of a new one.
		chunkBytes := *chunkMB << 20
		if *chunkMB == 0 {
			chunkBytes = c.autoChunk(*parallel)
			fmt.Printf("auto chunk size: %d MiB x %d parallel\n", chunkBytes>>20, *parallel)
		}
		_, err = c.json("POST", "/api/uploads", map[string]any{
			"filename": baseName(*file), "path": *dest, "size": size, "chunk_size": chunkBytes,
			"overwrite": *overwrite, "sha256": whole,
		}, &s)
		check(err)
		if s.ConfirmedChunks > 0 {
			fmt.Printf("resuming session %s (%d/%d chunks already confirmed)\n", s.ID, s.ConfirmedChunks, s.TotalChunks)
		} else {
			fmt.Printf("created session %s: %d chunks of %d MiB\n", s.ID, s.TotalChunks, s.ChunkSize>>20)
		}
	}

	have := make(map[int64]bool)
	for _, r := range s.ConfirmedRanges {
		for i := r[0]; i <= r[1]; i++ {
			have[i] = true
		}
	}
	todo := make(chan int64)
	var sent, confirmed int64
	var failed atomic.Value
	var corruptOnce sync.Once
	var wg sync.WaitGroup
	for w := 0; w < *parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range todo {
				if failed.Load() != nil {
					continue
				}
				off := idx * s.ChunkSize
				n := s.ChunkSize
				if off+n > size {
					n = size - off
				}
				sum, err := fileSHA(f, off, n)
				if err != nil {
					failed.Store(err)
					continue
				}
				times := 1
				if *dupes {
					times = 2
				}
				for rep := 0; rep < times; rep++ {
					bad := false
					if idx == *corrupt {
						corruptOnce.Do(func() { bad = true })
					}
					if err := c.sendChunk(s.ID, idx, func() io.Reader { return io.NewSectionReader(f, off, n) }, n, sum, bad, *retries); err != nil {
						failed.Store(err)
						break
					}
				}
				atomic.AddInt64(&sent, n)
				done := atomic.AddInt64(&confirmed, 1)
				if *killAfter > 0 && int(done) >= *killAfter {
					fmt.Printf("TEST: simulating client crash after %d chunks\n", done)
					os.Exit(3)
				}
				el := time.Since(start).Seconds()
				fmt.Printf("\rchunk %d/%d  %.1f MiB/s  rss=%dMiB   ", idx+1, s.TotalChunks, float64(atomic.LoadInt64(&sent))/(1<<20)/el, rssMiB())
			}
		}()
	}
	t0 := time.Now()
	for i := int64(0); i < s.TotalChunks; i++ {
		if !have[i] {
			todo <- i
		}
	}
	close(todo)
	wg.Wait()
	if e := failed.Load(); e != nil {
		fmt.Println()
		check(e.(error))
	}
	fmt.Printf("\nuploaded %d chunks in %.1fs\n", confirmed, time.Since(t0).Seconds())

	// Finalize and wait for the server to store the last parts and publish.
	finalizeErrs := 0
	for {
		var v session
		code, err := c.json("POST", "/api/uploads/"+s.ID+"/finalize", map[string]string{"sha256": whole}, &v)
		if err != nil {
			// Finalize is idempotent, so transient failures (network, 5xx, 429,
			// Cloudflare 52x) are simply retried; anything else is final.
			if finalizeErrs++; finalizeErrs <= 30 && (code == 0 || code == 429 || code >= 500) {
				fmt.Printf("finalize: transient error (%v), retrying\n", err)
				time.Sleep(time.Duration(2+rand.Intn(4)) * time.Second)
				continue
			}
			check(err)
		}
		finalizeErrs = 0
		fmt.Printf("finalize: %d state=%s parts=%d/%d retries=%d\n", code, v.State, v.PartsStored, v.TotalParts, v.RetryCount)
		if v.State == "finalized" {
			s = v
			break
		}
		if v.State == "failed" {
			check(fmt.Errorf("upload failed: %s", v.FailureReason))
		}
		time.Sleep(3 * time.Second)
	}
	fmt.Printf("published file id=%d name=%q total=%.1fs\n", *s.FileID, s.FinalName, time.Since(start).Seconds())

	if *verify {
		fmt.Println("verifying: downloading and hashing the stored file...")
		resp, err := c.do("GET", "/api/files/"+strconv.FormatInt(*s.FileID, 10)+"/stream", nil, -1, nil)
		check(err)
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			check(fmt.Errorf("download: HTTP %d", resp.StatusCode))
		}
		h := sha256.New()
		n, err := io.Copy(h, resp.Body)
		check(err)
		got := hex.EncodeToString(h.Sum(nil))
		if got != whole || n != size {
			fmt.Printf("VERIFY FAILED: size %d/%d sha %s != %s\n", n, size, got, whole)
			os.Exit(1)
		}
		fmt.Println("VERIFY OK: downloaded bytes match the source sha256")
	}
}

func (c *client) sendChunk(id string, idx int64, open func() io.Reader, n int64, sum string, corrupt bool, attempts int) error {
	began := time.Now()
	for a := 0; ; a++ {
		r := open() // fresh read of the file for every attempt
		if corrupt && a == 0 {
			b, _ := io.ReadAll(r)
			b[len(b)/2] ^= 0xff
			r = bytes.NewReader(b)
		}
		resp, err := c.do("PUT", "/api/uploads/"+id+"/chunks/"+strconv.FormatInt(idx, 10), r, n, map[string]string{
			"X-Chunk-SHA256": sum, "Content-Type": "application/octet-stream", "Expect": "100-continue"})
		var retryAfter time.Duration
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				// Trust only an explicit acknowledgement of this exact chunk. A bare
				// 200 can come from a proxy or a "starting up" page.
				var ack struct {
					Index *int64 `json:"index"`
				}
				if json.Unmarshal(raw, &ack) == nil && ack.Index != nil && *ack.Index == idx {
					return nil
				}
				err = fmt.Errorf("HTTP 200 without a chunk acknowledgement")
				goto retry
			}
			if s, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil {
				retryAfter = time.Duration(s) * time.Second
			}
			// 4xx other than 408/409/429 are permanent, except a corrupted
			// transfer (422) which is retried with a fresh read.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 409 && resp.StatusCode != 429 && resp.StatusCode != 422 {
				return fmt.Errorf("chunk %d rejected: HTTP %d", idx, resp.StatusCode)
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	retry:
		// Backpressure (429) and "not ready" (503) are the server asking us to
		// wait, not failures: they do not consume attempts, only the time budget.
		if retryAfter > 0 {
			if time.Since(began) > waitBudget {
				return fmt.Errorf("chunk %d: server still busy after %s", idx, waitBudget)
			}
			a--
		} else if a+1 >= attempts {
			return fmt.Errorf("chunk %d failed after %d attempts: %v", idx, attempts, err)
		}
		d := backoff(max(a, 0), retryAfter)
		fmt.Printf("\nchunk %d attempt %d failed (%v), retrying in %s\n", idx, a+1, err, d.Round(time.Millisecond))
		time.Sleep(d)
	}
}

func baseName(p string) string {
	u := url.URL{Path: p}
	s := u.Path
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}

func rssMiB() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys >> 20
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
