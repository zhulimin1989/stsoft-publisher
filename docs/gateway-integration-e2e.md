# Gateway Integration E2E

Gateway integration scripts should start publisher with the tracked CI config,
not the gitignored local `config/publisher.cfg`:

```bash
./bin/publisher --config config/publisher.ci.cfg
```

`config/publisher.ci.cfg` is safe to commit and matches the gateway e2e
defaults:

- HTTP: `:9080`, override with `HTTP_PORT`.
- PostgreSQL: `postgres://publisher:publisher@127.0.0.1:5432/tokenplatform?sslmode=disable`, override with `PG_DSN`.
- Redis: `127.0.0.1:6379`, override with `REDIS_ADDR` / `REDIS_PASSWORD`.
- Caller bearer tokens match the gateway `publisher_gateway_e2e.sh` admin,
  user, billing, and SRE test tokens.

PostgreSQL must accept the configured DSN and include a database name. On a
fresh cluster the role needs `CREATEDB` so publisher startup can create the
target database before applying migrations. Redis must be reachable before the
publisher readiness endpoint can pass.

A minimal smoke check is:

```bash
make build
./bin/publisher --config config/publisher.ci.cfg
curl -fsS http://127.0.0.1:9080/healthz
```
