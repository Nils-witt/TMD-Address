Before any Go coding, review, debugging, troubleshooting, or setup task, load the `samber/cc-skills-golang@golang-how-to` skill first — it routes to whichever other Go skills the task needs.

# address-serv

An address lookup and management service: streets, house numbers and geo objects (named points such as hydrants, grouped by `group`) stored in PostgreSQL, served over a JSON HTTP API. A React admin UI is embedded in the binary and served at `/ui/`.

## Layout

- `cmd/address-serv/main.go`: entrypoint. Reads env (and `.env` via godotenv), sets up OIDC, opens the DB, runs migrations, wires the stores into `api.NewHandler`.
- `internal/api/`: HTTP layer on Gin (as in the API). Handlers are `gin.HandlerFunc`s; each resource has a `registerXRoutes(rg gin.IRouter, store)` file, and `get(rg, path, h)` registers GET and HEAD alike. `api.go` builds the chain: gzip (outside Gin, skipped for WebSockets) → logging → CORS (answers every OPTIONS preflight) → the public routes, or the `authed` group with proxy auth → auth → `requireReadAccess`. `newEngine` sets Gin to route like the `ServeMux` it replaced: escaped path matching (an escaped `/` in an external ID stays one segment), 405 for a known path, no redirects; `router_test.go` pins this. `writeError` aborts the chain, so middleware just calls it and returns. The principal lives in the request context (`setPrincipal`/`principalFrom`), not `c.Set`. Tests drive handlers and middleware through `serveGin` (`gin_test.go`).
  - `auth.go`: every route except `/docs`, `/ui*`, `/ui-config`, `/api/version`, `/auth/backchannel-logout` needs `Authorization: Bearer <token>`. That token is either an OIDC JWT (`oidc.go`) or an API token, recognised by `store.APITokenPrefix`.
  - `readonly.go`: read-only principals (`principal.ReadOnly`): OIDC JWTs from one of `OIDC_READONLY_ISSUER_URLS` (any audience; each issuer is verified against its own discovered key set), and requests without a token that a trusted proxy names in `PROXY_AUTH_HEADER` (from `TRUSTED_PROXIES` only; e.g. an mTLS proxy). `requireReadAccess` lets them GET only `/healthz` and the lookups in `readOnlyRoutes`, 403 otherwise. Universam's SPA calls address-serv this way.
  - `suggest.go`: `/api/suggest/*`, what an address form suggests while typed into (prefix first, bounded, one request per field; `store.SuggestStore`), and `/api/suggest/reverse`, the nearest places flattened. Their snake_case JSON is what Universam's SPA (`frontend/src/api/address.ts`) reads.
  - `transfer.go`: `GET /api/export` and `POST /api/import` (`store.TransferStore`), a JSON document of streets with their house numbers and geo objects, keyed by natural keys rather than IDs so it moves between instances. Both filter by source (`manual=true`, repeatable `collector=<id>`), and the rows selected rows need always come along. Imported rows are always manual; rows a collector owns are never changed.
  - `backchannel_logout.go`: `POST /auth/backchannel-logout` (no auth: the logout token is verified against `OIDC_ISSUER_URL` for `OIDC_LOGOUT_AUDIENCE`), and the revocation check `authMiddleware` and the collector event stream apply to OIDC tokens.
  - `openapi.yaml`: the API spec, embedded and served at `/docs`. **This is the source of truth.** The root-level `openapi.yaml` is obsolete.
  - `spa/`: serves the embedded SPA with an index.html fallback.
- `internal/store/`: GORM (pgx driver) data access, one `XStore` per resource. `migrate.go` runs `AutoMigrate` and adds raw-SQL indexes on every startup. It needs the `cube` and `earthdistance` extensions for nearest-neighbour queries via `ll_to_earth`.
- `internal/logout/`: back-channel logouts in effect (`Service.Revoked` by `sub`/`sid`/`iat`), stored in `provider_logouts` (`store.ProviderLogoutStore`, also the outbox) and reloaded every 5 s. `internal/logoutbus/` shares them with the API and the tile server over RabbitMQ (`AMQP_URL`; fanout exchange `oidc.logout`, durable queue `oidc.logout.address`, JSON with `schema_version` 1). Its broker test runs only with `TEST_AMQP_URL`.
- `internal/servicebus/`: service discovery over the same broker (topic exchange `tmd.services`, HMAC-signed with `SERVICE_DISCOVERY_SECRET`). address-serv announces `SERVICE_URL` and answers token requests of `SERVICE_TOKEN_CLIENTS` with `store.APITokenStore.ReplaceServiceToken` (owner `service:<client>`), wired in `cmd/address-serv/discovery.go`. The package is a verbatim copy of the tile server's (also in the API and the units server); keep all copies identical.
- `frontend/`: Vite + React 19 + MUI + TanStack Query + i18next (locales `en` and `de`). `frontend/embed.go` embeds `frontend/dist`. `dist/.gitkeep` keeps the embed compiling before the first build.
- `internal/collector/`: collectors, executables in `COLLECTORS_DIR` that return addresses for the app to sync. `contract/` holds the contract types (documented in `docs/collectors/contract.md`), the rest discovers collectors and runs them as processes (Unix only). The sync itself is `store.CollectorStore.Sync`. Rows have a `collector_id`; API writes to owned rows answer 409.
- `import/`: Python scripts that load external data (such as Bonn's open data) through `POST /api/bulk`. They need `API_TOKEN` set.

## Commands

```sh
# Backend
docker compose up -d postgres           # local DB on POSTGRES_PORT (5438 in .env.example)
cp .env.example .env                    # then set OIDC_ISSUER_URL (required)
go run ./cmd/address-serv
go build ./... && go test -race -shuffle=on ./...
golangci-lint run ./cmd/... ./internal/...
govulncheck ./cmd/... ./internal/...
go mod tidy -diff                       # CI fails if go.mod isn't tidy

# Frontend (run from frontend/)
npm ci
npm run dev                             # proxies /api, /ui-config, /healthz, /docs to :8080
npm run build                           # tsc -b && vite build, output goes to dist/ and is embedded by Go
npm run lint && npm run format:check    # oxlint + prettier; run `npm run format` to fix
```

The husky pre-commit hook runs golangci-lint and govulncheck when they are installed.

## Tests

- Unit tests (validation, parsing) always run.
- Store tests, API integration tests and benchmarks need a real PostgreSQL and **are skipped unless `ADDRESS_SERV_TEST_DSN` is set**. They **wipe the database**, so never point it at one with data you care about:
  ```sh
  ADDRESS_SERV_TEST_DSN=postgres://user:pass@localhost:5438/address_serv_test go test ./...
  ADDRESS_SERV_TEST_DSN=... go test -run '^$' -bench . -benchmem ./internal/store
  ```
- Tests are table-driven and use `t.Parallel()`. Use the standard library `testing` package; there is no testify.
- A green `go test` run without the DSN has not exercised the DB code. Say so when reporting results.

## Conventions

- **A feature change touches every layer.** That means the store method and its test, the API handler and its test, `internal/api/openapi.yaml`, and, when the feature is user-facing, the frontend (`src/api/ApiClient.ts` + `types.ts`, a hook in `src/hooks/`, the page or dialog, and **both** `en` and `de` translation files).
- Errors: the store returns `store.ErrNotFound` (mapped from `gorm.ErrRecordNotFound` by `notFound`). With `TranslateError: true`, constraint violations surface as `gorm.ErrDuplicatedKey` and `gorm.ErrForeignKeyViolated`; handlers map them to 404 or 409 with `writeError`. Wrap errors with `%w`.
- `SkipDefaultTransaction` is on. Any write that needs more than one statement must run in an explicit `db.Transaction`.
- Schema changes go into `Migrate`, which must stay idempotent (`IF NOT EXISTS`, and safe on existing databases holding data). For a new uniqueness rule, follow `migrateGeoObjectGroupNameKey`: report the conflicting rows clearly rather than failing with a terse SQL error.
- golangci-lint is strict (`.golangci.yml`: wsl_v5, godot, funlen, gocyclo, dupl, gosec, …). Exported identifiers need doc comments that end with a period. Comments explain *why*, as the existing code does.
- Build info (`version`, `commit`, `branch`, `buildTime`) is injected with `-ldflags -X main.…` in the Dockerfile and `.goreleaser.yml`; `internal/buildinfo` fills empty values from the embedded VCS data. `GET /api/version` serves it in the same shape as the api's.
- Configuration comes only from environment variables. Document each new one in `.env.example` and `docker-compose.yml`.
