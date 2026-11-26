# tokenplatform-publisher

`tokenplatform-publisher` is an independent Go service that is the only writer
for gateway data-plane Redis (`gw:*` keys). Caller services (portal-server,
billing-service, ops-server, SRE oncall) send writes through the publisher's
HTTP API. The service durably stores each write in a PostgreSQL outbox and a
background worker drains the outbox into Redis with at-least-once delivery.


```text
caller-service --> HTTP API --> PG outbox --> worker --> Redis --> gateway nodes
```

## Repository layout

```text
cmd/
  publisher/              service entrypoint
  publisher-token/        helper for generating bearer tokens
internal/
  api/                    route registration + HTTP handlers
  config/                 config loading and env overrides
  dbinit/                 startup DB bootstrap + migration runner
  metrics/                Prometheus metrics and poller
  outbox/                 PG outbox store + replay worker
  publisher/              publisher interface + implementation
  types/                  request / data-plane payload types
config/
  publisher.example.cfg   example runtime config
deploy/
  docker-compose.yml      local PG/Redis dependencies
docs/
  api-reference.md
  interface-spec.md
migrations/
  0001_publisher_outbox.*
```

## Local development

```bash
# 1. Start dependencies
make up

# 2. Prepare config
cp config/publisher.example.cfg config/publisher.cfg

# 3. Run the service — startup auto-creates the database (if missing)
#    and applies pending migrations from migrations/*.up.sql before
#    serving traffic.
make build
./bin/publisher --config config/publisher.cfg
```

The DSN credentials need `CREATEDB` privilege only on the very first
launch against an empty cluster; once the database exists the bootstrap
is a no-op `SELECT FROM pg_database`. Migrations use a PG advisory lock,
so concurrent process starts are safe.

## Build and test

```bash
make build
make test
```

## Tokens

The `[tokens]` config section maps bearer tokens to service identities.
Generate development tokens with:

```bash
make new-tokens
make new-tokens COUNT=2 PREFIX=portal
```

## Operational notes

- `/healthz` and `/metrics` are public and should be protected at the network layer.
- This service is write-only relative to gateway Redis; it does not serve data-plane reads.
- Multi-region fan-out remains the responsibility of the control-plane layer.
