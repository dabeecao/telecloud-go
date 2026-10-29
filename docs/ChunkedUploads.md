# 📦 Resumable Chunked Uploads / Tải lên theo từng phần có thể tiếp tục

> 🇻🇳 Tóm tắt: API `/api/uploads` chia tệp lớn thành nhiều request nhỏ (mặc định 32 MiB, tối đa 50 MiB) để vượt qua giới hạn body của Cloudflare Tunnel (100 MB), có kiểm tra SHA-256 từng phần, tiếp tục được sau khi mất kết nối hoặc khởi động lại máy chủ. Chi tiết bằng tiếng Anh bên dưới.

Proxies such as **Cloudflare Tunnel** reject request bodies above a plan limit (100 MB on Free/Pro). TeleCloud's upload sessions split a file into **HTTP transport chunks**, each far below that limit, and store the result as one logical file.

> **Size limits.** Chunking removes the per-request bottleneck. It does **not** make storage unlimited: the maximum logical file size is bounded by Telegram's current upload/download constraints, available storage, database capacity, throughput, and the successful recovery of every stored part. Very large files (terabyte scale) should be validated end to end in your own deployment before production use.

## How it works

```
client ──PUT 32 MiB chunk──▶ staging file ──all chunks of a part present──▶ Telegram message ──▶ staging file deleted
         (sha256 verified)    (fsync, row in DB)                              (message_id in DB)
                                                  all parts stored + whole-file sha256 OK ──▶ file published (one transaction)
```

* **Transport chunks** (HTTP) and **storage parts** (Telegram) are separate. A storage part is the largest multiple of the chunk size that fits the account limit (e.g. 59 × 32 MiB = 1888 MiB for a 1900 MiB limit), so a chunk never straddles two parts.
* A part is sent to Telegram as soon as its chunks arrive, then its staging file is removed. Local disk use is therefore bounded by `CHUNKED_UPLOAD_STAGED_PARTS × part size`, **independent of the file size**. Memory use is bounded by a small copy buffer per in-flight chunk request.
* Chunk progress, part state and the running whole-file SHA-256 state are persisted. After a restart `Resume` continues without repeating completed work.
* The file appears in the file browser **only** after every part is stored and the size and SHA-256 match.

## Endpoints

All endpoints are under `/api`, use the normal session cookie (+ CSRF header) **or** HTTP Basic auth, and are scoped to the authenticated user.

| Method | Path | Purpose |
|---|---|---|
| GET | `/uploads/config` | Effective settings (threshold, chunk size, parallelism). Clients use chunked upload for files above `threshold_bytes`. |
| GET | `/uploads` | Your unfinished sessions (to resume after losing the ID). |
| POST | `/uploads/probe` | Speed probe: the body (up to 8 MiB) is read and discarded, response `{"bytes": N}`. Used to time an upload before choosing a chunk size. Nothing is stored. |
| POST | `/uploads` | Create a session. JSON: `filename`, `path`, `size`, optional `chunk_size`, `mime_type`, `overwrite`, `sha256` (whole file). Idempotent when `sha256` is given: an identical open upload (same file, destination, size and checksum) returns the existing session, so a client that lost its ID resumes by repeating the request. The returned session keeps its original chunk size, so clients must use `chunk_size` from the response, not the value they requested. |
| GET | `/uploads/:id` | Status / resume point. |
| PUT | `/uploads/:id/chunks/:index` | Upload one chunk. Raw body, `Content-Length`, header `X-Chunk-SHA256: <hex>`. |
| POST | `/uploads/:id/finalize` | Verify and publish. JSON `{"sha256": "<hex>"}` (optional if given at creation). Idempotent. |
| DELETE | `/uploads/:id` | Cancel and discard temporary data. A finalized upload cannot be cancelled. |

### Status fields

`state` (`open`, `finalizing`, `finalized`, `failed`, `cancelled`), `total_size`, `bytes_received`, `confirmed_chunks`, `total_chunks`, `confirmed_ranges` (inclusive `[first,last]` pairs), `parts_stored`/`total_parts`, `throughput_bytes_per_sec`, `retry_count`, `failure_reason`, `file_id`, `expires_at`. Logs contain only the first 8 characters of the session ID and counters — never credentials, file names or content.

### Responses to know

| Code | Meaning | Client action |
|---|---|---|
| 200 `{"index":N,"duplicate":bool}` | Chunk N is durably recorded. | Continue. Only this JSON confirms a chunk — a bare 200 from a proxy does not. |
| 400 `chunk_length_mismatch` | Body length ≠ expected. | Re-send. |
| 409 `chunk_in_progress` + `Retry-After` | A previous attempt for this chunk is still being received (typical after a proxy gave up on a slow request). | Wait, retry — not a failure. |
| 409 `chunk_conflict` | Index already stored with different content. | Abort/restart the session. |
| 422 `chunk_checksum_mismatch` | Body does not match `X-Chunk-SHA256` (corruption in transit). | Re-send. |
| 429 + `Retry-After` | Backpressure (`server_busy`, `too_many_parallel_chunks`, `staging_window_full`). | Wait, retry. |
| 503 + `Retry-After` | Telegram not ready (e.g. starting up). | Wait, retry. |
| 409 `incomplete_upload` | Finalize with missing chunks (lists the first missing). | Upload them, finalize again. |
| 202 (finalize) | Last parts are still being stored. | Poll `GET /uploads/:id` or call finalize again until `finalized` (HTTP 200). |

Re-sending a confirmed chunk with identical content is a harmless no-op, so retries after an unknown outcome are always safe.

**Tip: send `Expect: 100-continue` with chunk uploads.** Backpressure (`429`) is decided before the body is read. Without `Expect`, a client that is still streaming a 32 MiB body may see a connection reset instead of the `429` and its `Retry-After`. With `Expect: 100-continue` the server answers before any body is sent. Treat a reset like any other retryable failure, and retry the same chunk.

### Example (curl)

```bash
SIZE=$(stat -c %s big.iso); SHA=$(sha256sum big.iso | cut -d' ' -f1)
ID=$(curl -s -u user:pass -H 'Content-Type: application/json' \
  -d "{\"filename\":\"big.iso\",\"path\":\"/\",\"size\":$SIZE,\"sha256\":\"$SHA\"}" \
  https://cloud.example.com/api/uploads | jq -r .id)
# chunk i (32 MiB each)
dd if=big.iso bs=33554432 skip=0 count=1 2>/dev/null > /tmp/c0
curl -u user:pass -X PUT --data-binary @/tmp/c0 \
  -H "X-Chunk-SHA256: $(sha256sum /tmp/c0 | cut -d' ' -f1)" \
  https://cloud.example.com/api/uploads/$ID/chunks/0
curl -u user:pass -X POST https://cloud.example.com/api/uploads/$ID/finalize
```

A complete client with parallel workers, resume, retry/backoff and fault injection lives in [`tools/chunkupload`](../tools/chunkupload/main.go):

```bash
TC_USER=me TC_PASS=... go run ./tools/chunkupload -url https://cloud.example.com -file big.iso -path /Backups -verify-download
```

## Choosing the chunk size (auto-sizing)

A chunk request must finish within the proxy's timeout (about 100 s on Cloudflare Tunnel), otherwise the proxy answers 524/502 and the chunk is retried without making progress. How long a chunk takes depends on the client's uplink, so a fixed size is either too big for slow links or needlessly small for fast ones.

* `CHUNKED_UPLOAD_CHUNK_SIZE_MB` is the **ceiling** (up to 50 MiB, the hard maximum). Setting it to `50` is safe: clients choose a smaller chunk when their connection needs it.
* `GET /uploads/config` returns `chunk_size_bytes` (the ceiling), `min_chunk_size_bytes` (the smallest chunk the server accepts, 1 MiB; clients may use a higher floor such as 4 MiB to limit the request count) and `target_chunk_seconds` (40). A client should time a `POST /uploads/probe` upload, then use

  `chunk = clamp(speed_bytes_per_sec x target_chunk_seconds / parallel_streams, min, ceiling)`, rounded down to whole MiB,

  and send it as `chunk_size` when creating the session. The chunk size is fixed for the life of a session (a resumed session keeps its own).
* The reference client does this by default (`-chunk-mb 0`; pass `-chunk-mb N` to force a size) and the web UI does the same before it starts a large upload. Example: about 3 MiB/s with 3 parallel streams gives 40 MiB chunks; 1.6 MiB/s gives 21 MiB; 1 MiB/s gives 13 MiB; very slow links go down to the client's floor (4 MiB in the reference client).
* If the probe fails, clients fall back to 16 MiB (or the ceiling if lower).

## Configuration

See [Configuration.md](Configuration.md#chunked-upload) for the variables. Recommended for Cloudflare Tunnel: set `CHUNKED_UPLOAD_CHUNK_SIZE_MB=50` (the ceiling) and keep 3–4 parallel; clients shrink the chunk automatically on slow links (see above). Clients that do not auto-size (for example a plain `curl` loop) should pick a chunk small enough to finish well within about 100 s, e.g. 8–16 MiB below ~2 MiB/s.

## Cleanup and safety

* Sessions idle longer than `CHUNKED_UPLOAD_SESSION_TTL_HOURS` are removed together with their staging files and any Telegram parts that were never published.
* Cleanup only touches the session tables, its own staging directory (`CHUNKED_UPLOAD_DIR`, kept outside `TEMP_DIR`) and Telegram messages that no published file references. Finalized uploads lose only their bookkeeping row.
* If the server crashes while a part is being sent to Telegram, that one message may be left behind in the log group (the upload is retried and the file stays correct).
