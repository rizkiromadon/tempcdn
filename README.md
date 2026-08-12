# TempCDN Backend

Repository: https://github.com/rizkiromadon/tempcdn

> Working on the backend itself (adding a new endpoint/resource)? See
> [CONTRIBUTING.md](CONTRIBUTING.md) for the developer-facing walkthrough —
> this README documents the HTTP API from a client's point of view.


TempCDN is a login-free file upload backend. Files are stored physically in
Cloudflare R2, metadata is stored in Postgres (a single standalone instance
or multiple instances sharing one metadata store), and every file is
automatically removed after a fixed time-to-live (24 hours by default).
Expiry is enforced by an in-process background sweeper that deletes the R2
object and the DB row once a file's TTL has passed; an R2 Lifecycle Rule can
optionally be configured on the bucket as defense-in-depth, but the sweeper —
not the lifecycle rule — is what the application actually depends on for its
core "temporary" guarantee.

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
  - [Admin Authentication](#admin-authentication)
  - [API Keys](#api-keys)
  - [Upload Settings](#upload-settings)
  - [Legal Documents](#legal-documents)
  - [Config](#config)
  - [Stats](#stats)
  - [Node Status](#node-status)
  - [Upload a File](#upload-a-file)
  - [Get File Info](#get-file-info)
  - [Delete a File](#delete-a-file)
  - [Error Format](#error-format)
  - [CORS](#cors)
- [Design Notes](#design-notes)
  - [Rate limiting strategy](#rate-limiting-strategy)
  - [Streaming checksum vs. single-read upload](#streaming-checksum-vs-single-read-upload)
  - [Expiry enforcement](#expiry-enforcement)
  - [Admin auth: opaque sessions, not JWTs](#admin-auth-opaque-sessions-not-jwts)
- [Known Limitations](#known-limitations)
- [Running Multiple Instances](#running-multiple-instances)

## Features

- Anonymous file upload, no authentication required.
- Automatic expiry: every uploaded file is deleted after its TTL
  (`FILE_TTL_HOURS`, 24h by default) by a background sweeper that runs every
  `FILE_SWEEP_INTERVAL_MINUTES`. An R2 Lifecycle Rule can be layered on top
  as an out-of-band backstop, but is not required for correct behavior.
- Content-based deduplication: uploading identical file content twice will
  not re-upload the object to R2; the existing record is returned instead.
- MIME type sniffing and validation based on file content (not just file
  extension), plus a configurable blocked-extension list.
- Manual early deletion via the API, authorized by a one-time delete token
  issued at upload time (separate from the public file ID).
- Prometheus metrics (including per-route request latency) and structured
  JSON logging.
- Global concurrency limiter to protect server resources under load.

## Architecture

```
cmd/server             Application entry point
internal/config        Environment-based configuration loading
internal/httpserver    Router, middleware (logging, recovery, CORS), metrics
internal/upload        Upload handler, service, validator, checksum logic
internal/file          File info retrieval and deletion handler/service
internal/stats         Public usage-summary handler (GET /api/v1/stats)
internal/metadata      Postgres repository and file record model
internal/storage       Object storage interface and Cloudflare R2 client
internal/sweeper       Background expiry sweeper (deletes expired files)
internal/ratelimit     In-process concurrency limiter
internal/idgen         File ID generation
internal/response      Shared JSON response helpers
internal/logger        Structured logger setup
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
| `SERVER_MAX_UPLOAD_MB` | `100` | **Initial default only.** Seeds `max_upload_size_mb` the first time the server ever boots against a given database. After that, the live value lives in the `upload_settings` table and is changed via the admin API (`GET`/`PUT /api/v1/admin/upload-settings`), not by editing this variable — see [Upload Settings](#upload-settings) below. |
| `R2_ACCESS_KEY_ID` | — | R2 S3-compatible access key ID. Required. |
| `R2_SECRET_ACCESS_KEY` | — | R2 S3-compatible secret access key. Required. |
| `R2_BUCKET_NAME` | `tempcdn-files` | Target R2 bucket name. |
| `R2_ENDPOINT` | — | R2 S3-compatible endpoint URL. Required. |
| `R2_PUBLIC_BASE_URL` | — | Public base URL used to build `cdn_url` for uploaded objects. Required. |
| `DATABASE_DSN` | — | **Required.** Postgres connection string (`postgres://` or `postgresql://`). Required for every deployment, including a single standalone instance. For multiple instances sharing one metadata store, point every instance at the same database. See "Running Multiple Instances" below. |
| `DATABASE_MAX_CONNS` | `5` | Caps this instance's Postgres connection pool. Managed providers (e.g. Aiven's smaller tiers) often reserve only a handful of non-superuser connection slots — an uncapped or too-large pool can exhaust them, which fails startup with `remaining connection slots are reserved for roles with the SUPERUSER attribute`. In a multi-instance deployment this is effectively multiplied by the instance count. |
| `FILE_TTL_HOURS` | `24` | Hours before an uploaded file expires. |
| `FILE_SWEEP_INTERVAL_MINUTES` | `5` | How often the background sweeper checks for and deletes expired files. |
| `SERVER_MAX_CONCURRENT_UPLOADS` | `50` | Maximum number of uploads processed concurrently by this instance. This is a global, process-wide cap — not per-IP rate limiting (see [Rate limiting strategy](#rate-limiting-strategy)). The older name `RATE_LIMIT_MAX_CONCURRENT_UPLOADS` is still read as a fallback for one deprecation cycle. |
| `IP_HASH_SALT` | — | Salt used to hash the uploader's IP address before storing it. **Required** — the server refuses to start with the well-known default unless `ALLOW_INSECURE_IP_HASH_SALT=true` is also set (local development only). |
| `ALLOW_INSECURE_IP_HASH_SALT` | `false` | Set to `true` to allow starting without a real `IP_HASH_SALT`. Never set this in production. |
| `ALLOWED_ORIGIN` | — | Origin allowed to call this API from a browser. Required. |
| `ADMIN_BOOTSTRAP_USERNAME` | — | Username for the first admin dashboard account, created on startup only if no admin account exists yet. Safe to leave set across restarts/redeploys — a no-op once an admin exists. |
| `ADMIN_BOOTSTRAP_PASSWORD` | — | Password for the bootstrap admin account (min. 12 characters). If no admin account exists and this (and `ADMIN_BOOTSTRAP_USERNAME`) are unset, the server refuses to start. |
| `ALLOWED_MIME_TYPES` | `image/*,video/*,application/pdf,application/zip,text/plain` | **Initial default only** — same one-time-seed caveat as `SERVER_MAX_UPLOAD_MB` above. Comma-separated list of allowed MIME types/patterns. Supports `type/*` wildcards. |
| `BLOCKED_EXTENSIONS` | `.exe,.bat,.sh,.msi,.dll,.scr` | **Initial default only** — same one-time-seed caveat as `SERVER_MAX_UPLOAD_MB` above. Comma-separated list of blocked file extensions. Matched both as the file's final extension and as a substring earlier in the filename (e.g. `evil.exe.png` is also blocked). |
| `NODE_ID` | random `hostname-xxxx` | This instance's identifier in the node liveness table (`GET /api/v1/nodes`). Set explicitly (e.g. `srv1`, `srv2`) when running multiple instances, for stable/readable rows across restarts. |
| `NODE_HEARTBEAT_INTERVAL_SECONDS` | `15` | How often this instance updates its own liveness row. |
| `NODE_STALE_AFTER_SECONDS` | `45` | How long a node's heartbeat can go stale before another instance flags it offline. Must be greater than `NODE_HEARTBEAT_INTERVAL_SECONDS`. |
| `NODE_JANITOR_INTERVAL_SECONDS` | `20` | How often this instance checks all nodes for staleness. |

## API Reference

Base path: `/api/v1`

### Health Check

Returns service liveness status.

```
GET /healthz
HEAD /healthz
```

`HEAD` returns the same `200 OK` status and `Content-Type` as `GET`, without
a response body — useful for uptime/monitoring checks that poll frequently
and don't need the body.

**Response `200 OK`** (GET)

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

Requests must include valid credentials as either an `X-Metrics-Token`
header or an `Authorization: Bearer <token>` header. The value can be
**either**:

- a valid admin session token obtained from `POST /api/v1/admin/login`
  (see [Admin Authentication](#admin-authentication) below), or
- a valid, non-revoked API key created from `POST /api/v1/admin/api-keys`
  (see [API Keys](#api-keys) below)

There is no environment variable for this: API keys are database-backed
and revocable from the admin dashboard, so a compromised or unused key can
be revoked immediately without redeploying the server.

### Admin Authentication

Username/password login for the admin dashboard API, backed by
server-side, revocable sessions (database rows, not JWTs) — see
[Design Notes](#design-notes) for why.

The first admin account is created automatically on startup from
`ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD`, **only if no admin
account exists yet**. It's safe to leave both set across restarts and
redeploys: every boot after the first is a no-op here. If no admin account
exists and these are unset, the server refuses to start.

#### Log in

```
POST /api/v1/admin/login
Content-Type: application/json

{ "username": "admin", "password": "..." }
```

**Response `200 OK`**

```json
{
  "token": "5f2c...e91a",
  "username": "admin",
  "expires_at": "2026-08-12T09:00:00Z"
}
```

**Response `401 Unauthorized`** — invalid username or password. The error
message is identical for "no such user" and "wrong password", so the
endpoint can't be used to enumerate valid usernames.

Sessions expire 24 hours after login (or after last use — see below); after
that, log in again to get a new token. There is no separate refresh-token
flow, since sessions are cheap, revocable rows rather than long-lived JWTs.

#### Authenticated requests

Send the token from `login` as a Bearer token on every subsequent admin
request:

```
Authorization: Bearer 5f2c...e91a
```

Every authenticated request also refreshes the session's "last used"
timestamp.

#### Check current session

```
GET /api/v1/admin/me
Authorization: Bearer <token>
```

**Response `200 OK`**

```json
{ "username": "admin" }
```

**Response `401 Unauthorized`** — missing, invalid, or expired token.

#### Log out

```
POST /api/v1/admin/logout
Authorization: Bearer <token>
```

Revokes the session immediately (the token can't be used again, even
before its 24-hour expiry). Idempotent — logging out an already-invalid or
unknown token still returns `200 OK`.

**Response `200 OK`**

```json
{ "logged_out": true }
```

### API Keys

Long-lived, revocable credentials for server-to-server access — currently
used to gate `GET /metrics` (see [Metrics](#metrics)) — managed from the
admin dashboard rather than a static environment variable. Like admin
sessions, only a key's SHA-256 hash is ever stored; the plaintext key is
shown exactly once, in the response to `POST /api/v1/admin/api-keys`, and
can never be retrieved again afterward — only revoked and replaced with a
new key. All endpoints below require a valid admin session
(`Authorization: Bearer <session token>`, see
[Authenticated requests](#authenticated-requests) above).

#### Create a key

```
POST /api/v1/admin/api-keys
Authorization: Bearer <session token>
Content-Type: application/json

{ "name": "prometheus-prod" }
```

`name` is a free-text label for identifying the key later in the dashboard
(e.g. which scrape config or service it belongs to) — it has no bearing on
what the key can access.

**Response `201 Created`**

```json
{
  "id": "b2b1...9e3c",
  "name": "prometheus-prod",
  "key": "tcdn_5f2c...e91a",
  "created_at": "2026-08-11T09:00:00Z"
}
```

The `key` field is only ever returned here. Store it immediately (e.g. in
your Prometheus scrape config's `Authorization` header, or as
`X-Metrics-Token`) — there is no way to view it again.

**Response `400 Bad Request`** — `name` was empty.

#### List keys

```
GET /api/v1/admin/api-keys
Authorization: Bearer <session token>
```

Returns every key that has ever been created, active and revoked alike,
most recently created first. The plaintext key is never included.

**Response `200 OK`**

```json
[
  {
    "id": "b2b1...9e3c",
    "name": "prometheus-prod",
    "created_at": "2026-08-11T09:00:00Z",
    "last_used_at": "2026-08-11T10:15:00Z"
  },
  {
    "id": "a1c4...7d02",
    "name": "old-monitoring-box",
    "created_at": "2026-05-01T12:00:00Z",
    "revoked_at": "2026-07-01T08:00:00Z"
  }
]
```

`last_used_at` is refreshed on every successful authentication with that
key, so you can tell whether a key is actually still in use before
revoking it. `revoked_at` is present once a key has been revoked;
otherwise omitted.

#### Revoke a key

```
DELETE /api/v1/admin/api-keys/{id}
Authorization: Bearer <session token>
```

Revokes the key immediately — it can no longer authenticate, even before
any notion of expiry, since API keys don't expire on their own. The row
itself is kept (not deleted) so revoked keys still show up in the list
above, preserving a record of what existed and when it was revoked.
Idempotent: revoking an already-revoked or unknown ID still returns
`200 OK`.

**Response `200 OK`**

```json
{ "revoked": true }
```

### Upload Settings

Reads and changes the runtime-configurable upload limits enforced by every
instance sharing this database: maximum upload size, allowed MIME types,
and blocked file extensions. These used to be fixed for the life of the
process, set only via `SERVER_MAX_UPLOAD_MB` / `ALLOWED_MIME_TYPES` /
`BLOCKED_EXTENSIONS` at boot; they're now stored in the `upload_settings`
table and can be changed from the admin dashboard without a restart or
redeploy. The environment variables still matter — they seed the initial
row the very first time a server boots against a fresh database — but
after that, this API is the source of truth. All endpoints below require a
valid admin session (`Authorization: Bearer <session token>`, see
[Authenticated requests](#authenticated-requests) above).

Changes take effect immediately on the instance that handled the `PUT`
request. In a multi-instance deployment (see "Running Multiple Instances"),
every other instance sharing the same database picks up the change within
10 seconds (`upload.SettingsSynchronizer`, which polls `upload_settings`
in the background) — no restart needed. This means it's normal for a
`GET /api/v1/config` or an upload against a *different* instance than the
one that handled the `PUT` to briefly (up to ~10s) still reflect the old
limits.

#### Get current settings

```
GET /api/v1/admin/upload-settings
Authorization: Bearer <session token>
```

**Response `200 OK`**

```json
{
  "max_upload_size_mb": 100,
  "allowed_mime_types": ["image/*", "video/*", "application/pdf", "application/zip", "text/plain"],
  "blocked_extensions": [".exe", ".bat", ".sh", ".msi", ".dll", ".scr"],
  "updated_at": "2026-08-11T09:00:00Z",
  "updated_by": "a1c4...7d02"
}
```

`updated_by` is the admin ID who last changed these settings via `PUT`
below, and is omitted if the row still holds its original boot-time seed
and has never been changed since.

#### Update settings

```
PUT /api/v1/admin/upload-settings
Authorization: Bearer <session token>
Content-Type: application/json

{
  "max_upload_size_mb": 250,
  "allowed_mime_types": ["image/*", "video/*", "application/pdf"],
  "blocked_extensions": [".exe", ".bat", ".sh", ".msi", ".dll", ".scr"]
}
```

All three fields are required on every request — this isn't a partial/PATCH
update, so submit the full current settings (e.g. pre-filled from a prior
`GET`) with only the fields you want changed edited. Validation:

- `max_upload_size_mb` must be a positive integer, and at most 10240 (10 GiB).
- `allowed_mime_types` must contain at least one entry after trimming
  whitespace and dropping empty strings — an empty allowlist would silently
  reject every upload.
- Every entry in `blocked_extensions` must start with `.` (e.g. `.exe`, not
  `exe`) — a bare `exe` would never match and would silently be a no-op.
  This list may otherwise be empty.

**Response `200 OK`** — same shape as the `GET` response above, reflecting
the newly saved values.

**Response `400 Bad Request`** — the submitted settings failed one of the
checks above; the error message identifies which one.

### Legal Documents

Reads and changes admin-editable legal document content (terms of service,
privacy policy). Each document is stored as a single row in the
`legal_documents` table, seeded on first boot with placeholder text, and
edited afterward via the admin API — no redeploy needed. There is one
public read endpoint and one admin read/write pair per document.

#### Get terms of service (public)

```
GET /api/v1/legal/terms
```

**Response `200 OK`**

```json
{
  "doc_type": "terms",
  "content": "...",
  "updated_at": "2026-08-11T09:00:00Z",
  "updated_by": "a1c4...7d02"
}
```

`updated_by` is the admin ID who last changed the document via `PUT` below,
and is omitted if the document still holds its original boot-time seed.

#### Get privacy policy (public)

```
GET /api/v1/legal/privacy
```

Same response shape as `GET /api/v1/legal/terms` above, with
`"doc_type": "privacy"`.

#### Get / update terms of service (admin)

```
GET /api/v1/admin/legal/terms
Authorization: Bearer <session token>
```

```
PUT /api/v1/admin/legal/terms
Authorization: Bearer <session token>
Content-Type: application/json

{
  "content": "..."
}
```

`content` must not be empty (or whitespace-only). **Response `200 OK`** —
same shape as the public `GET` above, reflecting the newly saved content.
**Response `400 Bad Request`** if `content` is empty.

#### Get / update privacy policy (admin)

```
GET /api/v1/admin/legal/privacy
Authorization: Bearer <session token>
```

```
PUT /api/v1/admin/legal/privacy
Authorization: Bearer <session token>
Content-Type: application/json

{
  "content": "..."
}
```

Same validation and response shape as the terms endpoints above, for the
`privacy` document.

### Config

Returns the server's current upload constraints, so a client can validate a
file locally (size, MIME type) before attempting an upload rather than
discovering a rejection only after sending the bytes. Always public, like
`/stats`.

```
GET /api/v1/config
```

**Response `200 OK`**

```json
{
  "max_upload_size_bytes": 104857600,
  "max_upload_size_mb": 100,
  "allowed_mime_types": ["image/*", "video/*", "application/pdf", "application/zip", "text/plain"],
  "blocked_extensions": [".exe", ".bat", ".sh", ".msi", ".dll", ".scr"],
  "file_ttl_hours": 24
}
```

`max_upload_size_mb`, `allowed_mime_types`, and `blocked_extensions` reflect
the live values from [Upload Settings](#upload-settings) above (initially
seeded from `SERVER_MAX_UPLOAD_MB` / `ALLOWED_MIME_TYPES` /
`BLOCKED_EXTENSIONS` — see [Configuration](#configuration) — but changeable
afterward via the admin API without a restart). `file_ttl_hours` still
mirrors `FILE_TTL_HOURS` directly, since that value isn't runtime-configurable.

### Stats

Returns a JSON usage summary. Unlike `/metrics` (Prometheus text exposition
format, optionally token-gated), this endpoint is plain JSON and always
public, like `/config` — it's aggregate/non-sensitive usage data, not
per-file detail.

```
GET /api/v1/stats
```

**Response `200 OK`**

```json
{
  "active_file_count": 42,
  "active_bytes": 104857600,
  "average_file_bytes": 2496609,
  "content_type_breakdown": { "image": 30, "video": 10, "other": 2 },
  "lifetime_uploads_total": 1337,
  "lifetime_upload_bytes_total": 5368709120,
  "lifetime_upload_errors_total": 12,
  "generated_at": "2026-08-10T09:00:00Z"
}
```

- `active_*` and `content_type_breakdown` reflect files that exist in the
  metadata store right now — they fall as files expire or are deleted early,
  the same way `GET /api/v1/files/{id}` would stop finding them.
- `lifetime_*` fields are sourced from the same Prometheus counters backing
  `/metrics` (`tempcdn_uploads_total`, `tempcdn_upload_bytes_total`,
  `tempcdn_upload_errors_total`). They only increase — they are **not**
  reduced when files expire or are deleted — and reset to `0` on process
  restart, since they aren't persisted independently of the metadata store.

### Node Status

Returns a read-only liveness view of every tempcdn instance sharing this
deployment's database (see [Running Multiple Instances](#running-multiple-instances)).
Public, like `/config` and `/stats` — operational visibility, not sensitive
per-file data.

```
GET /api/v1/nodes
```

**Response `200 OK`**

```json
{
  "nodes": [
    {
      "node_id": "srv1",
      "hostname": "srv1.internal",
      "status": "online",
      "started_at": "2026-08-09T00:00:00Z",
      "last_heartbeat_at": "2026-08-11T09:00:12Z",
      "seconds_since_heartbeat": 3.4
    },
    {
      "node_id": "srv2",
      "hostname": "srv2.internal",
      "status": "offline",
      "started_at": "2026-08-08T00:00:00Z",
      "last_heartbeat_at": "2026-08-10T22:14:01Z",
      "marked_offline_at": "2026-08-10T22:15:00Z",
      "seconds_since_heartbeat": 39251.9
    }
  ],
  "generated_at": "2026-08-11T09:00:15Z"
}
```

- A single standalone instance still reports its own row here — it just
  never sees any peers.
- `status` is only ever `"online"` or `"offline"`. A node never marks
  itself offline (a crashed or powered-off process can't run that code);
  instead, any other still-live instance's background janitor flips a row
  to `"offline"` once its `last_heartbeat_at` goes stale
  (`NODE_STALE_AFTER_SECONDS`, see [Configuration](#configuration)), and
  stamps `marked_offline_at` at that moment.
- `seconds_since_heartbeat` is computed at response time from the server's
  own clock, so a polling client doesn't need its clock synced with the
  server's to tell how stale a node's heartbeat currently is.
- A node that comes back online after being flagged offline reclaims
  `"online"` status the moment it heartbeats again.

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

- File must not be empty and must not exceed the current `max_upload_size_mb`
  (see [Upload Settings](#upload-settings) — initially seeded from
  `SERVER_MAX_UPLOAD_MB`, but live-configurable afterward via the admin API).
- File extension must not be in the current `blocked_extensions` list.
- Content type is detected by sniffing the file's magic bytes (not by trusting
  the client-supplied `Content-Type`) and must match a pattern in the current
  `allowed_mime_types` list.
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
  "duplicate": false,
  "delete_token": "6f1c1a6e2e0a4c9f8b1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b"
}
```

`delete_token` is shown **only in this response** — it is not persisted in
plaintext and cannot be recovered later. Save it if you may need to delete
the file early; without it, the file can still be read via `GET`, but not
deleted until it naturally expires. `delete_token` is omitted (empty) when
`duplicate` is `true`, since the original uploader already holds the real
one.

**Error responses**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing `file` field, invalid multipart data, file empty, file too large, blocked extension, or disallowed content type. |
| `503 Service Unavailable` | Server has reached `SERVER_MAX_CONCURRENT_UPLOADS` concurrent uploads. Retry shortly. |
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

Deletes a file before its TTL expires. Requires the delete token returned
in the original upload response.

```
DELETE /api/v1/files/{id}
```

**Path parameters**

| Parameter | Description |
|---|---|
| `id` | The file's unique ID. |

**Headers**

| Header | Required | Description |
|---|---|---|
| `X-Delete-Token` | Yes | The delete token returned at upload time. Alternatively, pass it as a `delete_token` query parameter, though the header is preferred since query parameters are more likely to end up in access logs or browser history. |

**Example request**

```bash
curl -X DELETE http://localhost:8080/api/v1/files/b6b3f6d2-9b1a-4e8b-8a7a-2e6c9e6b0a11 \
  -H "X-Delete-Token: 6f1c1a6e2e0a4c9f8b1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b"
```

**Response `200 OK`**

```json
{ "deleted": true }
```

**Error responses**

| Status | Condition |
|---|---|
| `403 Forbidden` | Missing or incorrect delete token. |
| `404 Not Found` | No file exists with the given ID. |
| `410 Gone` | File exists but has already expired. |
| `500 Internal Server Error` | Unexpected server-side failure. |

Knowing a file's ID is not sufficient to delete it — the ID is necessarily
shared (it's embedded in the CDN URL and the `GET` endpoint), so it doesn't
double as a delete credential. Only the uploader, who received the delete
token once at upload time, can delete the file early.

### Error Format

All error responses share the same JSON shape:

```json
{ "error": "human-readable error message" }
```

### CORS

- `POST /api/v1/upload` and `DELETE /api/v1/files/{id}` use a strict CORS
  policy, locked to `ALLOWED_ORIGIN` — configure it before deploying to
  production. The server refuses to start if `ALLOWED_ORIGIN` is unset.
- `GET /api/v1/files/{id}` uses a permissive CORS policy (`*`), since file
  metadata is not sensitive and is commonly read from arbitrary front-end
  origins.
- `GET /metrics` uses the strict, `ALLOWED_ORIGIN` CORS policy, plus an
  admin-session-or-API-key check that isn't CORS-dependent (see
  [Metrics](#metrics)) — CORS is a browser-enforced policy only and does
  nothing to stop direct/server-to-server requests.
- `GET /api/v1/stats` uses the strict, `ALLOWED_ORIGIN` CORS policy, same as
  `/config` — no token, since the data it returns is aggregate/non-sensitive.

## Design Notes

### Rate limiting strategy

Per-IP rate limiting is intentionally **not** implemented at the application
level. This origin server is expected to sit behind Cloudflare, and a
Cloudflare Rate Limiting Rule at the edge is more effective: it blocks abusive
traffic before it reaches the origin (saving bandwidth and compute), and has
a more trustworthy view of the client IP than HTTP headers, which can be
spoofed if the origin is reachable directly without going through the proxy.

What the backend does provide:

- **A global concurrency limiter** (`SERVER_MAX_CONCURRENT_UPLOADS`) —
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

### Expiry enforcement

Expiry is enforced by `internal/sweeper`, a ticker-driven background
goroutine started in `main.go` that runs every `FILE_SWEEP_INTERVAL_MINUTES`
(default 5). Each tick:

1. Queries up to 100 records whose `expires_at` has passed, oldest first.
2. Deletes the R2 object for each. If this fails, the DB row is left in
   place so the record is retried on the next tick rather than the app
   losing track of an object that's still live in the bucket.
3. Deletes the corresponding DB row.
4. If Cloudflare cache purging is enabled, purges the deleted files' CDN
   URLs from the edge cache in one batched request.

The sweeper runs an initial pass immediately on startup (not just on the
first tick) so files that expired while the process was down don't linger
longer than necessary. An R2 Lifecycle Rule configured on the bucket
directly is a reasonable defense-in-depth addition on top of this, but the
application does not depend on one being configured correctly — the sweeper
is the actual, verifiable enforcement mechanism.

### Admin auth: opaque sessions, not JWTs

`POST /api/v1/admin/login` returns an opaque, random 256-bit token, not a
JWT. Only the token's SHA-256 hash is ever persisted (`admin_sessions.token_hash`)
— the same pattern already used for file delete tokens
(`files.delete_token_hash`) — so a database read or leak alone can't be
replayed as a valid session.

The deliberate tradeoff versus a stateless JWT is that every authenticated
request costs one extra database lookup (`FindAdminSessionByTokenHash`).
In exchange:

- **Sessions are individually revocable.** `POST /api/v1/admin/logout`
  deletes exactly one row, and the token is invalid on the very next
  request — no waiting out a JWT's expiry window.
- **No signing key to manage or rotate.** A leaked JWT signing key
  compromises every session past and future until rotated; there is no
  equivalent single point of failure here.
- A background janitor (`internal/admin.SessionJanitor`) periodically
  purges expired rows so the table doesn't grow unbounded from sessions
  that were never explicitly logged out.

`/metrics` accepts either an admin session token or a database-backed API
key (see [Metrics](#metrics) and [API Keys](#api-keys)) — both are checked
against the same `Authorization: Bearer` / `X-Metrics-Token` header, so a
caller supplies whichever it has, not both. API keys follow the same
hash-only storage and one-time-display model as session tokens, described
above.

## Known Limitations

- `go.sum` is not committed. The Dockerfile will fall back to `go mod tidy`
  when it detects no `go.sum` is present, but for fully reproducible,
  offline-cacheable builds, generate and commit one with `go mod tidy` on a
  machine with network access, then remove that fallback from the
  Dockerfile.
- The concurrency limiter is in-memory and per-instance. For real horizontal
  scaling, each instance enforces its own `SERVER_MAX_CONCURRENT_UPLOADS`
  independently — the effective global cap is roughly N × that value across
  N instances, not a single shared limit. For a real shared limit, replace
  it with a Redis-backed limiter.
- The expiry sweeper runs in-process on every instance's own ticker. When
  multiple instances share one Postgres database (see below), each
  instance's sweeper still ticks independently, but `FindExpired` uses
  `FOR UPDATE SKIP LOCKED` so two instances can never both claim the same
  expired row in overlapping sweeps — redundant *ticks* still happen, but
  not redundant *deletions* of the same record. With a single standalone
  instance this doesn't apply since there's only ever one sweeper.

## Running Multiple Instances

Running more than one tempcdn instance (e.g. `srv1.tempcdn.eu.cc`,
`srv2.tempcdn.eu.cc`, `srv3.tempcdn.eu.cc` behind a frontend that rotates
requests across them) requires every instance to share one metadata store,
or an upload made through one instance won't be visible — for GET, DELETE,
or dedup-by-checksum — through another. Every instance already talks to
Postgres (required for even a single standalone instance — see
`DATABASE_DSN` above), so multi-instance just means pointing them all at
the same database.

To run multiple instances:

1. Stand up one Postgres database reachable by every instance (a managed
   database, or Postgres running on one of the hosts with the others
   allowed to connect to it).
2. Set `DATABASE_DSN` on every instance to the same
   `postgres://user:password@host:5432/dbname?sslmode=require` value.
3. Set `IP_HASH_SALT` to the same value on every instance, so the same
   uploader IP hashes identically regardless of which instance handled the
   request (otherwise per-uploader dedup-adjacent logic and stats become
   inconsistent across instances — this does not affect correctness of file
   serving/deletion, only consistency of derived data).
4. All instances already point at the same R2 bucket by design (object
   storage was already shared before this), so no change is needed there.

See `docker-compose.multi.yml` for a runnable example of three instances
sharing one Postgres database, and `.env.example` for the `DATABASE_DSN`
formats for both modes.

Each instance still runs its own sweeper goroutine and its own in-memory
concurrency limiter; see Known Limitations above for what that does and
doesn't mean for correctness at multi-instance scale. Each instance also
runs its own `upload.SettingsSynchronizer`, polling `upload_settings`
every 10 seconds so an admin-configured limit change converges across
every instance without a restart (see [Upload Settings](#upload-settings)).
