# Contributing

This document is for developers working on the tempcdn backend itself. If
you're looking for the HTTP API reference, see [README.md](README.md).

## Table of Contents

- [Project layout](#project-layout)
- [Adding a new resource](#adding-a-new-resource)
  - [The short version](#the-short-version)
  - [Step by step](#step-by-step)
- [Why the repository is split by domain](#why-the-repository-is-split-by-domain)
- [Why routes are registered with `route()`](#why-routes-are-registered-with-route)
- [Testing conventions](#testing-conventions)

## Project layout

```
cmd/server             Application entry point, dependency wiring (main.go)
internal/config        Environment-based configuration loading
internal/httpserver    Router, middleware (logging, recovery, CORS), metrics
internal/upload        Upload handler, service, validator, checksum logic
internal/file          File info retrieval and deletion handler/service
internal/stats         Public usage-summary handler
internal/nodestatus    Multi-instance liveness reporting/handler
internal/admin         Admin auth, sessions, API keys, upload settings
internal/metadata      Domain models + repository interfaces + Postgres impl
internal/storage       Object storage interface and Cloudflare R2 client
internal/sweeper       Background expiry sweeper (deletes expired files)
internal/ratelimit     In-process concurrency limiter
internal/idgen         ID/token generation
internal/response      Shared JSON response helpers
internal/logger        Structured logger setup
```

Every feature follows the same layering: **repository** (persistence) →
**service** (business logic) → **handler** (HTTP) → **router** (wiring).
Look at `internal/nodestatus` (simple read-only resource) or
`internal/stats` (another small read-only handler) as existing, live
examples of this layering when in doubt.

## Adding a new resource

### The short version

1. Add a domain model + narrow repository interface in `internal/metadata`.
2. Implement that interface on `*PostgresRepository` in its own
   `postgres_repository_<domain>.go` file, plus a migration.
3. Write a `Service` in a new `internal/<yourpackage>` that depends only on
   the interface from step 1 — never on the full `metadata.Repository`.
4. Write a `Handler` in the same package that depends only on `*Service`.
5. Wire it up: construct the handler in `cmd/server/main.go`, then register
   it with `reg.route(...)` in `internal/httpserver/router.go`.
6. Test the service with a small fake implementing just your interface from
   step 1 — it should be a handful of methods, not the whole repository.

That's it — no step touches more than the one file it owns, and nothing
outside your new package needs to change.

### Step by step

Say you're adding a resource called `widget` (swap in your real name). The
example below sketches each file for a minimal `POST /api/v1/widgets` +
`GET /api/v1/widgets` pair.

**Step 1 — model + interface** (`internal/metadata/widget_model.go`)

```go
package metadata

import (
	"context"
	"time"
)

type Widget struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// WidgetRepository is intentionally narrow: only what internal/widget
// actually needs, not "everything you could ever do with the widgets
// table". A narrow interface is what keeps a test fake for it small.
type WidgetRepository interface {
	InsertWidget(ctx context.Context, w *Widget) error
	ListWidgets(ctx context.Context, limit int) ([]*Widget, error)
}
```

**Step 2 — Postgres implementation + migration**
(`internal/metadata/postgres_repository_widgets.go`, plus
`internal/metadata/postgres_migrations/NNNN_widgets.sql` where `NNNN` is
the next unused number — check the directory for the current highest)

```go
package metadata

func (r *PostgresRepository) InsertWidget(ctx context.Context, w *Widget) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO widgets (id, name, created_at) VALUES ($1, $2, $3)`,
		w.ID, w.Name, w.CreatedAt)
	return err
}

func (r *PostgresRepository) ListWidgets(ctx context.Context, limit int) ([]*Widget, error) {
	// query, scan into []*Widget, return
}
```

This lives as plain methods on `*PostgresRepository`, in its own file
alongside the other `postgres_repository_<domain>.go` files. The migration
you add runs automatically on the next `Migrate()` call in `main.go` — no
separate migration command to remember.

**Step 3 — service** (`internal/widget/service.go`)

```go
package widget

type Service struct {
	repository metadata.WidgetRepository // not metadata.Repository
}

func NewService(repository metadata.WidgetRepository) *Service {
	return &Service{repository: repository}
}
```

Depending on `metadata.WidgetRepository` instead of `metadata.Repository`
is the one habit that keeps this whole approach paying off over time — see
[Why the repository is split by domain](#why-the-repository-is-split-by-domain).

**Step 4 — handler** (`internal/widget/handler.go`)

The handler depends only on `*Service`, never on `metadata` directly. It
decodes the request, calls the service, and shapes the JSON response —
nothing else. See `internal/nodestatus/handler.go` for a small worked
example of this shape.

**Step 5 — wire it up** in `cmd/server/main.go` and
`internal/httpserver/router.go`:

```go
// main.go
widgetService := widget.NewService(repository) // repository already satisfies metadata.WidgetRepository
widgetHandler := widget.NewHandler(widgetService)
```

`repository` here is the same `*metadata.PostgresRepository` already
constructed for everything else — because it embeds every domain
interface via the `Repository` union (see `internal/metadata/repository.go`),
it automatically satisfies `metadata.WidgetRepository` too, no extra
wiring needed there. Pass `widgetHandler` into `httpserver.RouterDependencies`
(add a field for it), then in `router.go`'s `NewRouter`, inside the
`apiRouter.Route("/api/v1", ...)` block:

```go
apiReg.route(apiRouter, origin, http.MethodPost, "/widgets", deps.WidgetHandler.Create)
apiReg.route(apiRouter, origin, http.MethodGet, "/widgets", deps.WidgetHandler.List)
```

That's the entire router change — CORS and the `OPTIONS` preflight for
`/widgets` are handled automatically by `route()` (see below).

**Step 6 — test** (`internal/widget/service_test.go`), against a small
fake implementing just `metadata.WidgetRepository`:

```go
type fakeRepository struct {
	inserted []*metadata.Widget
}

func (f *fakeRepository) InsertWidget(ctx context.Context, w *metadata.Widget) error {
	f.inserted = append(f.inserted, w)
	return nil
}

func (f *fakeRepository) ListWidgets(ctx context.Context, limit int) ([]*metadata.Widget, error) {
	return f.inserted, nil
}
```

Two methods. That's the whole fake. It doesn't know files, admins, nodes,
or api keys exist, because `Service` never asked it to.

**Optional — embed into the top-level union.** If you want
`WidgetRepository` embedded in the top-level `Repository` union (so e.g.
some other cross-cutting code could depend on it too), add
`metadata.WidgetRepository` to the interface list in `metadata.Repository`
in `repository.go`. Most new resources don't need this — only add it if
something outside your own package actually needs the combined interface.

## Why the repository is split by domain

Before this refactor, `metadata.Repository` was one interface with every
method for every table — files, node status, admins, sessions, API keys,
upload settings — all in one place. Every new feature had to add a method
to that one interface, implement it in one large `postgres_repository.go`,
and every test fake in the codebase had to grow a new stub method to keep
compiling, whether or not that fake's service actually used it.

Now each domain has its own interface (`FileRepository`,
`NodeStatusRepository`, `AdminRepository`, `APIKeyRepository`,
`UploadSettingsRepository`), and `metadata.Repository` is just the union of
all of them for `cmd/server/main.go`'s convenience. Concrete effects of
this:

- **Fakes stay small.** `internal/stats`'s test fake implements exactly the
  6 methods of `FileRepository` it needs — not 29.
- **A service's dependencies are self-documenting.** Seeing
  `repository metadata.FileRepository` in `file.Service` tells you, without
  reading further, that it cannot touch admin sessions or node status —
  the type system enforces it.
- **Nothing outside your new file changes.** Adding a new domain interface
  doesn't require touching `file.Service`, `admin.Service`, or any existing
  test — they don't import it and never will unless they choose to.

`*metadata.PostgresRepository` still implements every interface (it's one
struct, split across `postgres_repository*.go` files purely for
readability) — `main.go` doesn't need to change how it constructs things.

## Why routes are registered with `route()`

Before this refactor, every endpoint needed two separate, easy-to-desync
lines in `router.go`: one to register the handler with a specific CORS
middleware, and a second, manually-written `Options(...)` call for the
preflight response — and both had to independently list the correct HTTP
methods.

`reg.route(router, origin, method, pattern, handler, ...middleware)`
(defined in `internal/httpserver/middleware.go`) does both in one call: it
registers the handler under the right CORS policy for that method, and
automatically (re-)registers a combined `OPTIONS` preflight for that
pattern — so two calls sharing a pattern (e.g. `GET` and `POST` both on
`/api-keys`) merge into one correct preflight advertising both methods,
instead of you hand-writing `"GET, POST, OPTIONS"` somewhere and hoping it
stays in sync.

Use `reg.route(...)` for any endpoint with a single CORS policy per method
— which is nearly everything. The one exception in this codebase is
`/files/{id}`, which needs two genuinely different CORS policies on the
same path (a permissive one for `GET`, a strict one for `DELETE`) — that
one is wired by hand in `router.go` with a small comment explaining why.

## Testing conventions

- Test a `Service` against a small, package-local fake implementing only
  the narrow repository interface it depends on (see `internal/stats` for
  an example) — not against a real database and not against a fake for the
  full `metadata.Repository`.
- Postgres-backed integration tests (see
  `internal/metadata/postgres_repository_test.go`) skip themselves via
  `t.Skip` when `TEST_POSTGRES_DSN` isn't set, so `go test ./...` works
  without a database for everyday development.
- Run `go test ./...` before sending a change. Run it with
  `TEST_POSTGRES_DSN` set against a scratch database to also exercise the
  Postgres-backed tests.
