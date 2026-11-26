package config

import (
	"testing"
	"time"
)

// Production mounts publisher-config as environment only and ships no cfg
// file, so these overrides are the sole way to tune or switch off a janitor
// that deletes rows. Without them the feature would be unreachable once
// deployed.

func TestLoad_RetentionDefaultsWithoutConfigFile(t *testing.T) {
	cfg, _, err := Load("testdata/does-not-exist.cfg")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Worker.JanitorInterval != time.Hour {
		t.Errorf("JanitorInterval = %v, want 1h", cfg.Worker.JanitorInterval)
	}
	if cfg.Worker.JanitorBatch != 5000 {
		t.Errorf("JanitorBatch = %d, want 5000", cfg.Worker.JanitorBatch)
	}
	if cfg.Worker.JanitorMaxBatches != 20 {
		t.Errorf("JanitorMaxBatches = %d, want 20", cfg.Worker.JanitorMaxBatches)
	}
}

func TestLoad_RetentionEnvOverrides(t *testing.T) {
	t.Setenv("PUBLISHER_JANITOR_INTERVAL_MINUTES", "15")
	t.Setenv("PUBLISHER_JANITOR_BATCH", "250")
	t.Setenv("PUBLISHER_JANITOR_MAX_BATCHES", "4")
	t.Setenv("PUBLISHER_DELIVERED_RETAIN_HOURS", "72")
	t.Setenv("PUBLISHER_DEAD_LETTER_RETAIN_HOURS", "240")

	cfg, ovr, err := Load("testdata/does-not-exist.cfg")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Worker.JanitorInterval != 15*time.Minute {
		t.Errorf("JanitorInterval = %v, want 15m", cfg.Worker.JanitorInterval)
	}
	if cfg.Worker.JanitorBatch != 250 || cfg.Worker.JanitorMaxBatches != 4 {
		t.Errorf("bounds = %d x %d, want 250 x 4", cfg.Worker.JanitorBatch, cfg.Worker.JanitorMaxBatches)
	}
	if cfg.Worker.DeliveredRetention != 72*time.Hour {
		t.Errorf("DeliveredRetention = %v, want 72h", cfg.Worker.DeliveredRetention)
	}
	if cfg.Worker.DeadLetterRetain != 240*time.Hour {
		t.Errorf("DeadLetterRetain = %v, want 240h", cfg.Worker.DeadLetterRetain)
	}
	if !ovr.JanitorIntervalApplied || !ovr.JanitorBoundsApplied || !ovr.RetentionWindowsApplied {
		t.Errorf("overrides not reported: %+v", ovr)
	}
}

// Zero is the documented off switch and must survive as zero, because
// NewWorker treats a zero interval as "retention disabled".
func TestLoad_JanitorIntervalZeroDisables(t *testing.T) {
	t.Setenv("PUBLISHER_JANITOR_INTERVAL_MINUTES", "0")

	cfg, ovr, err := Load("testdata/does-not-exist.cfg")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Worker.JanitorInterval != 0 {
		t.Errorf("JanitorInterval = %v, want 0 so retention stays off", cfg.Worker.JanitorInterval)
	}
	if !ovr.JanitorIntervalApplied {
		t.Error("JanitorIntervalApplied = false, want the disable to be reported at startup")
	}
}

// A malformed knob must fail startup rather than silently fall back to a
// retention policy the operator did not choose.
func TestLoad_RetentionEnvRejectsBadValues(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"PUBLISHER_JANITOR_INTERVAL_MINUTES", "-1"},
		{"PUBLISHER_JANITOR_INTERVAL_MINUTES", "hourly"},
		{"PUBLISHER_JANITOR_BATCH", "0"},
		{"PUBLISHER_JANITOR_MAX_BATCHES", "-3"},
		{"PUBLISHER_DELIVERED_RETAIN_HOURS", "forever"},
		{"PUBLISHER_DEAD_LETTER_RETAIN_HOURS", "0"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, _, err := Load("testdata/does-not-exist.cfg"); err == nil {
				t.Fatalf("Load accepted %s=%q, want an error", tc.key, tc.val)
			}
		})
	}
}
