// publisher service entry. Wires:
//
//   - pgxpool to PG (outbox storage)
//   - go-redis client to Redis (worker writes)
//   - publisher.Impl: outbox-backed Publisher V2
//   - api.Server: HTTP layer mounting all service endpoints
//   - outbox.Worker: background retry loop draining outbox -> Redis
//
// Configuration is read from CONFIG (default ./config/publisher.cfg).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/api"
	"github.com/tokenplatform/publisher/internal/config"
	"github.com/tokenplatform/publisher/internal/dbinit"
	"github.com/tokenplatform/publisher/internal/metrics"
	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/refunddrain"
	"github.com/tokenplatform/publisher/migrations"
)

func main() {
	// First thing: structured JSON to stdout so every subsequent log line
	// — INCLUDING startup failures — flows through the same pipeline as
	// runtime logs. Anything written via fmt or stderr would be invisible
	// to JSON-only log shippers.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfgPath := flag.String("config", envOr("CONFIG", "config/publisher.cfg"), "path to config file")
	flag.Parse()

	slog.Info("publisher starting", "cfg_path", *cfgPath, "pid", os.Getpid())

	cfg, envOvr, err := config.Load(*cfgPath)
	if err != nil {
		fail("load_config", err)
	}
	slog.Info("config loaded",
		"http_port", cfg.HTTP.Port,
		"pg_dsn_set", cfg.PG.DSN != "",
		"redis_addr_host", redisAddrHost(cfg.Redis.Addr),
		"callers_total", len(cfg.Callers),
	)
	logEnvOverrides(envOvr)

	// Build auth BEFORE touching PG/Redis: pure in-memory, fail-fast on
	// caller misconfig, and emit `callers_detail` so operators can verify
	// CALLERS_JSON / [caller:*] really applied — the previous silent path
	// hid this until first request 401-ed.
	auth, err := api.NewTokenAuth(cfg.Callers)
	if err != nil {
		fail("build_token_auth", err)
	}
	slog.Info("token auth ready",
		"callers_total", len(cfg.Callers),
		"callers_detail", auth.ConfiguredCallers(),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Bootstrap: create the target database if missing, then bring schema
	// up to the latest embedded migration. Both calls are idempotent — on
	// already-current databases this is two round-trips and zero writes.
	if err := dbinit.EnsureDatabase(ctx, cfg.PG.DSN); err != nil {
		fail("ensure_database", err)
	}
	if err := dbinit.Migrate(ctx, cfg.PG.DSN, migrations.FS); err != nil {
		fail("migrate", err)
	}
	slog.Info("database ensured + migrated")

	pgCfg, err := pgxpool.ParseConfig(cfg.PG.DSN)
	if err != nil {
		fail("parse_pg_dsn", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		fail("open_pg_pool", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fail("pg_ping", err)
	}
	slog.Info("pg pool ready")

	// Redis address accepts either host:port form or a full URL
	// (redis://... / rediss://...). The URL form is what AWS ElastiCache
	// surfaces in the console; ParseURL handles auth, db, TLS in one go.
	var rdb *redis.Client
	if strings.HasPrefix(cfg.Redis.Addr, "redis://") || strings.HasPrefix(cfg.Redis.Addr, "rediss://") {
		opt, err := redis.ParseURL(cfg.Redis.Addr)
		if err != nil {
			fail("parse_redis_url", err)
		}
		if cfg.Redis.Password != "" {
			opt.Password = cfg.Redis.Password
		}
		if cfg.Redis.DB != 0 {
			opt.DB = cfg.Redis.DB
		}
		rdb = redis.NewClient(opt)
	} else {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
	}
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fail("redis_ping", err)
	}
	slog.Info("redis ping ok", "addr_host", redisAddrHost(cfg.Redis.Addr))

	store := outbox.NewStore(pool)
	pub := publisher.New(store)
	dedicatedValidation, err := publisher.DedicatedProviderValidationConfigFromStrings(
		cfg.DedicatedProviderValidation.ProviderIDFormat,
		cfg.DedicatedProviderValidation.ProviderExists,
		cfg.DedicatedProviderValidation.ProviderDedicatedAccess,
		cfg.DedicatedProviderValidation.ProviderProtocolBinding,
		cfg.DedicatedProviderValidation.ProviderOwnerScopeMatches,
	)
	if err != nil {
		fail("dedicated_provider_validation_config", err)
	}
	pub.SetDedicatedProviderValidationConfig(dedicatedValidation)
	pub.SetRuntimeProviderLookup(publisher.NewRedisRuntimeProviderLookup(rdb))
	slog.Info("dedicated provider validation ready",
		"provider_id_format", dedicatedValidation.ProviderIDFormat,
		"provider_exists", dedicatedValidation.ProviderExists,
		"provider_dedicated_access", dedicatedValidation.ProviderDedicatedAccess,
		"provider_protocol_binding", dedicatedValidation.ProviderProtocolBinding,
		"provider_owner_scope_matches", dedicatedValidation.ProviderOwnerScopeMatches,
	)
	// HA Commit B-3: wire Redis for balanceDedupe cache-hit verification.
	// Required in multi-instance deployments without sticky-key LB routing
	// to prevent cross-pod cache-staleness windows. See
	// docs/HA_DEPLOYMENT.md.
	pub.SetRedisForBalanceVerify(rdb)

	worker := outbox.NewWorker(store, rdb, outbox.WorkerConfig{
		ScanInterval:       cfg.Worker.ScanInterval,
		BatchPerTick:       cfg.Worker.BatchPerTick,
		MaxAttempts:        cfg.Worker.MaxAttempts,
		BaseDelay:          cfg.Worker.BaseDelay,
		MaxDelay:           cfg.Worker.MaxDelay,
		DeliveredRetention: cfg.Worker.DeliveredRetention,
		DeadLetterRetain:   cfg.Worker.DeadLetterRetain,
		JanitorInterval:    cfg.Worker.JanitorInterval,
		JanitorBatch:       cfg.Worker.JanitorBatch,
		JanitorMaxBatches:  cfg.Worker.JanitorMaxBatches,
	}, nil)
	go worker.Run(ctx)
	slog.Info("outbox worker started",
		"scan_interval", cfg.Worker.ScanInterval,
		"batch_per_tick", cfg.Worker.BatchPerTick,
		"max_attempts", cfg.Worker.MaxAttempts,
		"janitor_interval", cfg.Worker.JanitorInterval,
		"delivered_retention", cfg.Worker.DeliveredRetention,
		"dead_letter_retention", cfg.Worker.DeadLetterRetain,
	)

	poller := metrics.NewPoller(store, 0)
	go poller.Run(ctx)
	slog.Info("metrics poller started")

	server := api.NewServer(pub, auth)
	server.SetKeyAdmissionStrictMode(cfg.KeyAdmission.Strict)
	server.SetRefundDrainCoordinator(refunddrain.New(rdb))
	slog.Info("key admission mode ready", "strict", cfg.KeyAdmission.Strict)
	server.AddReadinessCheck("postgres", func(ctx context.Context) error { return pool.Ping(ctx) })
	server.AddReadinessCheck("redis", func(ctx context.Context) error { return rdb.Ping(ctx).Err() })

	httpSrv := &http.Server{
		Addr:              cfg.HTTP.Addr(),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("publisher http listening", "addr", cfg.HTTP.Addr())
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http serve", "error", err)
			cancel()
		}
	}()

	slog.Info("publisher startup complete")

	<-ctx.Done()
	slog.Info("publisher shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "error", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// redisAddrHost strips secrets from cfg.Redis.Addr for safe logging.
// Plain "host:port" passes through; "redis://:secret@host:port/0" form
// is reduced to its Host (username/password dropped). Used by the
// "config loaded" / "redis ping ok" log lines so the password embedded
// in URL-form addresses never lands in the log pipeline.
func redisAddrHost(addr string) string {
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		if u, err := url.Parse(addr); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return addr
}

// fail emits a structured startup-failure log AND exits non-zero. Goes
// through slog so the same JSON shipper that handles runtime logs
// captures it; the previous fmt.Fprintf(stderr) form was invisible to
// JSON-only log collectors. `stage` is a short snake-case marker so
// alerting can pattern-match on which init step blew up.
func fail(stage string, err error) {
	slog.Error("publisher startup failed", "stage", stage, "error", err.Error())
	os.Exit(1)
}

// logEnvOverrides emits one `env applied` Info line per env var that
// took effect during config.Load. Parse failures don't reach here —
// they fail-fast inside applyEnv (HTTP_PORT, CALLERS_JSON). Sensitive
// values are never emitted: PG_DSN / REDIS_PASSWORD log only "applied",
// REDIS_ADDR is redacted via redisAddrHost, CALLERS_JSON shows the
// service-name list but not their tokens (tokens already land masked
// in the subsequent `token auth ready` line).
func logEnvOverrides(ovr config.EnvOverrides) {
	if ovr.HTTPPortApplied {
		slog.Info("env applied", "var", "HTTP_PORT", "value", ovr.HTTPPort)
	}
	if ovr.PGDSNApplied {
		slog.Info("env applied", "var", "PG_DSN")
	}
	if ovr.RedisAddrApplied {
		slog.Info("env applied", "var", "REDIS_ADDR", "host", redisAddrHost(ovr.RedisAddr))
	}
	if ovr.RedisPasswordApplied {
		slog.Info("env applied", "var", "REDIS_PASSWORD")
	}
	if ovr.KeyAdmissionStrictApplied {
		slog.Info("env applied", "var", "PUBLISHER_KEY_ADMISSION_STRICT", "value", ovr.KeyAdmissionStrict)
	}
	if len(ovr.CallersServices) > 0 {
		slog.Info("env applied",
			"var", "CALLERS_JSON",
			"services_count", len(ovr.CallersServices),
			"services", ovr.CallersServices,
		)
	}
}
