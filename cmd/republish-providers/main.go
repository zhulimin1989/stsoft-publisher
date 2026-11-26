package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/config"
	"github.com/tokenplatform/publisher/internal/dbinit"
	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/migrations"
)

type republishOutput struct {
	DryRun                      bool                              `json:"dry_run"`
	Provider                    string                            `json:"provider,omitempty"`
	Scanned                     int                               `json:"scanned"`
	Changed                     int                               `json:"changed"`
	ManualIntervention          int                               `json:"manual_intervention"`
	ManualInterventionProviders []string                          `json:"manual_intervention_providers,omitempty"`
	Plans                       []publisher.ProviderRepublishPlan `json:"plans"`
	AppliedBatches              []appliedBatch                    `json:"applied_batches,omitempty"`
}

type appliedBatch struct {
	ProviderID string `json:"provider_id"`
	BatchID    string `json:"batch_id"`
}

func main() {
	cfgPath := flag.String("config", envOr("CONFIG", "config/publisher.cfg"), "path to config file")
	dryRun := flag.Bool("dry-run", true, "print providers that would change without enqueueing outbox writes")
	providerID := flag.String("provider", "", "optional provider id to republish instead of all providers")
	flag.Parse()

	ctx := context.Background()
	cfg, _, err := config.Load(*cfgPath)
	if err != nil {
		failf("load config: %v", err)
	}

	rdb, err := newRedisClient(cfg.Redis)
	if err != nil {
		failf("redis client: %v", err)
	}
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		failf("redis ping: %v", err)
	}

	rawProviders, err := loadProviderPayloads(ctx, rdb, strings.TrimSpace(*providerID))
	if err != nil {
		failf("load providers: %v", err)
	}
	plans, err := publisher.PlanProviderRepublishAll(rawProviders)
	if err != nil {
		failf("plan providers: %v", err)
	}

	out := republishOutput{
		DryRun:   *dryRun,
		Provider: strings.TrimSpace(*providerID),
		Scanned:  len(plans),
		Plans:    plans,
	}
	for _, plan := range plans {
		if plan.Changed {
			out.Changed++
		}
		if plan.NeedsManualIntervention {
			out.ManualIntervention++
			out.ManualInterventionProviders = append(out.ManualInterventionProviders, plan.ProviderID)
		}
	}

	if !*dryRun && out.Changed > 0 {
		pub, closeFn, err := newPublisher(ctx, cfg)
		if err != nil {
			failf("publisher: %v", err)
		}
		defer closeFn()
		for _, plan := range plans {
			if !plan.Changed {
				continue
			}
			batchID, err := publisher.ApplyProviderRepublishPlan(ctx, pub, plan)
			if err != nil {
				failf("apply provider %s: %v", plan.ProviderID, err)
			}
			out.AppliedBatches = append(out.AppliedBatches, appliedBatch{
				ProviderID: plan.ProviderID,
				BatchID:    batchID.String(),
			})
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		failf("write output: %v", err)
	}
}

func loadProviderPayloads(ctx context.Context, rdb *redis.Client, providerID string) (map[string]string, error) {
	if providerID != "" {
		payload, err := rdb.HGet(ctx, publisher.KeyProviders, providerID).Result()
		if err != nil {
			return nil, err
		}
		return map[string]string{providerID: payload}, nil
	}
	return rdb.HGetAll(ctx, publisher.KeyProviders).Result()
}

func newPublisher(ctx context.Context, cfg *config.Config) (publisher.Publisher, func(), error) {
	if err := dbinit.EnsureDatabase(ctx, cfg.PG.DSN); err != nil {
		return nil, nil, err
	}
	if err := dbinit.Migrate(ctx, cfg.PG.DSN, migrations.FS); err != nil {
		return nil, nil, err
	}
	pgCfg, err := pgxpool.ParseConfig(cfg.PG.DSN)
	if err != nil {
		return nil, nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return nil, nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, err
	}
	pub := publisher.New(outbox.NewStore(pool))
	return pub, pool.Close, nil
}

func newRedisClient(cfg config.RedisConfig) (*redis.Client, error) {
	if strings.HasPrefix(cfg.Addr, "redis://") || strings.HasPrefix(cfg.Addr, "rediss://") {
		opt, err := redis.ParseURL(cfg.Addr)
		if err != nil {
			return nil, err
		}
		if cfg.Password != "" {
			opt.Password = cfg.Password
		}
		if cfg.DB != 0 {
			opt.DB = cfg.DB
		}
		return redis.NewClient(opt), nil
	}
	if _, err := url.Parse("//" + cfg.Addr); err != nil {
		return nil, err
	}
	return redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
