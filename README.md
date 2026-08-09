# TempCDN Backend

TempCDN is a login-free file upload backend. Files are stored physically in
Cloudflare R2, metadata is stored in SQLite, and every file is automatically
removed after a fixed time-to-live (24 hours by default), enforced by an R2
Lifecycle Rule with a defensive `expires_at` check in the application layer.

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Getting Started](#getting-started)
  - [Run locally](#run-locally)
  - [Run with Docker](#run-with-docker)
  - [Run tests](#run-tests)
- [Configuration](#configuration)
- [API Reference](#api-reference)
  - [Health Check](#health-check)
  - [Metrics](#metrics)
  - [Upload a File](#upload-a-file)
  - [Get File Info](#get-file-info)
  - [Delete a File](#delete-a-file)
  - [Error Format](#error-format)
- [Design Notes](#design-notes)
  - [Rate limiting strategy](#rate-limiting-strategy)
  - [Streaming checksum vs. single-read upload](#streaming-checksum-vs-single-read-upload)
- [Known Limitations](#known-limitations)

## Features

- Anonymous file upload, no authentication required.
- Automatic expiry: every uploaded file is deleted 24 hours after upload
  (configurable) via an R2 Lifecycle Rule, with an application-level
  `expires_at` guard as a defensive backstop.
- Content-based deduplication: uploading identical file content twice will
  not re-upload the object to R2; the existing record is returned instead.
- MIME type sniffing and validation based on file content (not just file
  extension), plus a configurable blocked-extension list.
- Manual early deletion via the API, before the TTL expires.
- Prometheus metrics and structured JSON logging.
- Global concurrency limiter to protect server resources under load.

## Architecture

```
cmd/server            Application entry point
internal/config        Environment-based configuration loading
internal/httpserver     Router, middleware (logging, recovery, CORS), metrics
internal/upload         Upload handler, service, validator, checksum logic
internal/file            File info retrieval and deletion handler/service
internal/metadata       SQLite repository and file record model
internal/storage         Object storage interface and Cloudflare R2 client
internal/ratelimit      In-process concurrency limiter
internal/idgen           File ID generation
internal/response        Shared JSON response helpers
internal/logger           Structured logger setup
```

## Getting Started

### Run locally

```bash
cp .env.example .env
# fill in your R2 credentials in .env
go mod tidy
go run ./cmd/server
```

### Run with Docker

```bash
docker compose up --build
```

### Run tests

```bash
go test ./...
```

Test coverage includes `internal/upload/validator_test.go`,
`internal/upload/checksum_test.go`, `internal/upload/service_test.go`
(including the deduplication scenario — uploading the same file twice results
in `PutObject` being called only once, verified through a mock
`ObjectStorage`), and `internal/metadata/repository_test.go`.

## Configuration

All configuration is provided via environment variables. See `.env.example`
for a ready-to-copy template.

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `8080` | Port the HTTP server listens on. |
| `SERVER_MAX_UPLOAD_MB` | `100` | Maximum allowed upload size, in megabytes. |
| `R2_ACCOUNT_ID` | — | Cloudflare account ID. |
| `R2_ACCESS_KEY_ID` | — | R2 S3-compatible access key ID. Required. |
| `R2_SECRET_ACCESS_KEY` | — | R2 S3-compatible secret access key. Required. |
| `R2_BUCKET_NAME` | `tempcdn-files` | Target R2 bucket name. |
| `R2_ENDPOINT` | — | R2 S3-compatible endpoint URL. Required. |
| `R2_PUBLIC_BASE_URL` | — | Public base URL used to build `cdn_url` for uploaded objects. Required. |
| `DATABASE_DSN` | `file:tempcdn.db?cache=shared&_fk=1` | SQLite DSN. |
| `FILE_TTL_HOURS` | `24` | Hours before an uploaded file expires. |
| `RATE_LIMIT_MAX_CONCURRENT_UPLOADS` | `50` | Maximum number of uploads processed concurrently by this instance. |
| `IP_HASH_SALT` | `insecure-default-salt` | Salt used to hash the uploader's IP address before storing it. Set a strong random value in production. |
| `ALLOWED_MIME_TYPES` | `image/*,video/*,application/pdf,application/zip,text/plain` | Comma-separated list of allowed MIME types/patterns. Supports `type/*` wildcards. |
| `BLOCKED_EXTENSIONS` | `.exe,.bat,.sh,.msi,.dll,.scr` | Comma-separated list of blocked file extensions. |

## API Reference

Base path: `/api/v1`

### Health Check

Returns service liveness status.

```
GET /healthz
```

**Response `200 OK`**

```json
{ "status": "ok" }
```

### Metrics

Exposes Prometheus metrics (`tempcdn_uploads_total`,
`tempcdn_upload_bytes_total`, `tempcdn_upload_errors_total`,
`tempcdn_request_latency_seconds`, and default Go/process metrics).

```
GET /metrics
```

### Upload a File

Uploads a file as `multipart/form-data`. No authentication is required.

```
POST /api/v1/upload
Content-Type: multipart/form-data
```

**Form fields**

| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file | Yes | The file to upload. |

**Validation rules**

- File must not be empty and must not exceed `SERVER_MAX_UPLOAD_MB`.
- File extension must not be in `BLOCKED_EXTENSIONS`.
- Content type is detected by sniffing the file's magic bytes (not by trusting
  the client-supplied `Content-Type`) and must match a pattern in
  `ALLOWED_MIME_TYPES`.
- If the file content (SHA-256 checksum) matches an existing, non-expired
  file, no new object is uploaded to R2 — the existing record is returned
  with `duplicate: true`.

**Example request**

```bash
curl -X POST http://localhost:8080/api/v1/upload \
  -F "file=@photo.png;type=image/png"
```

**Response `200 OK`**

```json
{
  "id": "b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11",
  "original_name": "photo.png",
  "content_type": "image/png",
  "size_bytes": 20481,
  "checksum_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85",
  "object_key": "2026/08/09/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11.png",
  "cdn_url": "https://cdn.tempcdn.example.com/2026/08/09/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11.png",
  "created_at": "2026-08-09T10:15:00Z",
  "expires_at": "2026-08-10T10:15:00Z",
  "duplicate": false
}
```

**Error responses**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing `file` field, invalid multipart data, file empty, file too large, blocked extension, or disallowed content type. |
| `503 Service Unavailable` | Server has reached `RATE_LIMIT_MAX_CONCURRENT_UPLOADS` concurrent uploads. Retry shortly. |
| `504 Gateway Timeout` | Upload processing exceeded the request deadline. |
| `500 Internal Server Error` | Unexpected server-side failure. |

### Get File Info

Retrieves metadata for a previously uploaded file.

```
GET /api/v1/files/{id}
```

**Path parameters**

| Parameter | Description |
|---|---|
| `id` | The file's unique ID, returned from the upload response. |

**Example request**

```bash
curl http://localhost:8080/api/v1/files/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11
```

**Response `200 OK`**

```json
{
  "id": "b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11",
  "original_name": "photo.png",
  "content_type": "image/png",
  "size_bytes": 20481,
  "checksum_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85",
  "object_key": "2026/08/09/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11.png",
  "cdn_url": "https://cdn.tempcdn.example.com/2026/08/09/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11.png",
  "created_at": "2026-08-09T10:15:00Z",
  "expires_at": "2026-08-10T10:15:00Z",
  "expired": false
}
```

**Error responses**

| Status | Condition |
|---|---|
| `404 Not Found` | No file exists with the given ID. |
| `410 Gone` | File record exists but has already expired. The response body still contains the metadata, with `"expired": true`. |
| `500 Internal Server Error` | Unexpected server-side failure. |

### Delete a File

Deletes a file before its TTL expires.

```
DELETE /api/v1/files/{id}
```

**Path parameters**

| Parameter | Description |
|---|---|
| `id` | The file's unique ID. |

**Example request**

```bash
curl -X DELETE http://localhost:8080/api/v1/files/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11
```

**Response `200 OK`**

```json
{ "deleted": true }
```

**Error responses**

| Status | Condition |
|---|---|
| `404 Not Found` | No file exists with the given ID. |
| `410 Gone` | File exists but has already expired. |
| `500 Internal Server Error` | Unexpected server-side failure. |

### Error Format

All error responses share the same JSON shape:

```json
{ "error": "human-readable error message" }
```

### CORS

- `POST /api/v1/upload` and `DELETE /api/v1/files/{id}` use a strict CORS
  policy — configure the allowed origin before deploying to production.
- `GET /api/v1/files/{id}` uses a permissive CORS policy (`*`), since file
  metadata is not sensitive and is commonly read from arbitrary front-end
  origins.

## Design Notes

### Rate limiting strategy

Per-IP rate limiting is intentionally **not** implemented at the application
level. This origin server is expected to sit behind Cloudflare, and a
Cloudflare Rate Limiting Rule at the edge is more effective: it blocks abusive
traffic before it reaches the origin (saving bandwidth and compute), and has
a more trustworthy view of the client IP than HTTP headers, which can be
spoofed if the origin is reachable directly without going through the proxy.

What the backend does provide:

- **A global concurrency limiter** (`RATE_LIMIT_MAX_CONCURRENT_UPLOADS`) —
  this is not per-IP abuse protection, but a pure stability safety net that
  prevents too many parallel uploads from exhausting memory, goroutines, or
  file descriptors.
- **IP hashing** (`uploader_ip_hash` in the `files` table) is stored for audit
  purposes. The client IP is resolved with the following priority:
  `CF-Connecting-IP` header → `X-Forwarded-For` (first hop) → `RemoteAddr`.
  The `CF-Connecting-IP` header can only be fully trusted if the origin server
  is not reachable except through Cloudflare (e.g. via a firewall that
  restricts the origin to Cloudflare's IP ranges).

### Streaming checksum vs. single-read upload

The system needs to satisfy two requirements that are, taken literally, in
tension with each other:

1. The checksum must be computed while streaming the file (a single read
   pass, without buffering the whole file into memory).
2. If the file is a duplicate, `PutObject` must **never** be called against
   R2.

These two constraints cannot be satisfied purely at the same time — to know
the checksum (and therefore know whether to skip the upload), the entire file
must already have been read; but once it has been fully read, if it were
piped directly to R2 without waiting for the dedup decision, `PutObject`
would already have been called before the duplicate result is known.

The solution used in `internal/upload/service.go`: the file is spooled to a
temporary file on disk (not buffered fully into RAM) while its checksum is
computed in a single read pass using `io.TeeReader` (see
`internal/upload/checksum.go`). After that:

- If the checksum matches an existing active record, the existing result is
  returned immediately — `PutObject` is never called, and the temp file is
  removed.
- If no active duplicate exists, the temp file is seeked back to the start
  and streamed to R2 via a single `PutObject` call.

This satisfies both core constraints (the client never needs to upload
twice, and `PutObject` is genuinely skipped for duplicates), with the
trade-off that the file briefly resides on the server's local disk during
the upload process, rather than being piped purely memory-to-memory.

## Known Limitations

- `go.sum` is not committed and must be generated by running `go mod tidy`
  on a machine with network access before running `go build` or `go test`.
- The concurrency limiter is in-memory and per-instance. For real horizontal
  scaling, replace it with a Redis-backed limiter so behavior stays
  consistent across instances.
- The strict CORS policy currently defaults to an empty allowed origin
  (`StrictCORS("")`). Set a specific origin via configuration before
  deploying to production.
