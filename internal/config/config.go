package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EnvOverrides records which env vars applyEnv actually applied. Used by
// the caller (cmd/publisher) to emit one structured `env applied` log
// per overridden var at startup, so operators can see at a glance which
// settings came from env vs the cfg file — and so silent typos in env
// values surface immediately (HTTP_PORT / CALLERS_JSON parse errors are
// returned as Load errors rather than swallowed). Sensitive values
// (DSN, password, tokens) are NOT stored here; only "was it set" flags
// or already-safe scalars (port number, service-name list).
type EnvOverrides struct {
	HTTPPort                  int // override value; 0 + HTTPPortApplied=false means unset
	HTTPPortApplied           bool
	PGDSNApplied              bool   // never carry the DSN itself
	RedisAddr                 string // raw value; caller is expected to redact (URL form may contain password)
	RedisAddrApplied          bool
	RedisPasswordApplied      bool     // never carry the password
	CallersServices           []string // sorted service-name list from CALLERS_JSON
	KeyAdmissionStrict        bool
	KeyAdmissionStrictApplied bool
	// Retention overrides are surfaced so the startup log shows the effective
	// policy. A janitor that deletes rows must never be silently reconfigured.
	JanitorIntervalApplied  bool
	JanitorInterval         time.Duration
	JanitorBoundsApplied    bool
	RetentionWindowsApplied bool
}

// Config is a hand-rolled INI/TOML-flavored loader kept in stdlib-only form
// so the publisher can stay lightweight and easy to bootstrap.
type Config struct {
	HTTP                        HTTPConfig
	PG                          PGConfig
	Redis                       RedisConfig
	Worker                      WorkerConfig
	DedicatedProviderValidation DedicatedProviderValidationConfig
	KeyAdmission                KeyAdmissionConfig
	Callers                     map[string]Caller // service identity -> {namespace, bearer tokens}
}

// Caller couples a service identity's URL namespace with the bearer tokens
// it can authenticate as. Tokens list supports rotation (keep N concurrently
// valid). Namespace is one of: user / billing / admin / sre.
type Caller struct {
	Namespace string   `json:"namespace"`
	Tokens    []string `json:"tokens"`
}

type HTTPConfig struct {
	Port int
}

// Addr returns the bind address derived from Port (e.g. ":80").
func (h HTTPConfig) Addr() string {
	return fmt.Sprintf(":%d", h.Port)
}

type PGConfig struct {
	DSN string
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type WorkerConfig struct {
	ScanInterval       time.Duration
	BatchPerTick       int
	MaxAttempts        int
	BaseDelay          time.Duration
	MaxDelay           time.Duration
	DeliveredRetention time.Duration
	DeadLetterRetain   time.Duration
	// JanitorInterval is how often retention runs. 0 disables it, which is the
	// behaviour that shipped before the janitor was wired to Worker.Run.
	JanitorInterval   time.Duration
	JanitorBatch      int
	JanitorMaxBatches int
}

type DedicatedProviderValidationConfig struct {
	ProviderIDFormat          string
	ProviderExists            string
	ProviderDedicatedAccess   string
	ProviderProtocolBinding   string
	ProviderOwnerScopeMatches string
}

type KeyAdmissionConfig struct {
	Strict bool
}

// Load parses the on-disk config and applies env overrides. Returned
// EnvOverrides tells the caller which env vars actually took effect so
// it can emit per-var startup logs.
func Load(path string) (*Config, EnvOverrides, error) {
	cfg := &Config{
		HTTP: HTTPConfig{Port: 80},
		Redis: RedisConfig{
			Addr: "127.0.0.1:6379",
			DB:   0,
		},
		Worker: WorkerConfig{
			ScanInterval:       time.Second,
			BatchPerTick:       100,
			MaxAttempts:        12,
			BaseDelay:          time.Second,
			MaxDelay:           60 * time.Second,
			DeliveredRetention: 7 * 24 * time.Hour,
			DeadLetterRetain:   30 * 24 * time.Hour,
			JanitorInterval:    time.Hour,
			JanitorBatch:       5000,
			JanitorMaxBatches:  20,
		},
		DedicatedProviderValidation: DedicatedProviderValidationConfig{
			ProviderIDFormat:          "enforce",
			ProviderExists:            "enforce",
			ProviderDedicatedAccess:   "enforce",
			ProviderProtocolBinding:   "enforce",
			ProviderOwnerScopeMatches: "enforce",
		},
		Callers: map[string]Caller{},
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			ovr, err := cfg.applyEnv()
			if err != nil {
				return nil, ovr, err
			}
			return cfg, ovr, nil
		}
		return nil, EnvOverrides{}, err
	}

	section := ""
	for ln, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.TrimSpace(t[1 : len(t)-1])
			continue
		}
		eq := strings.IndexByte(t, '=')
		if eq < 0 {
			return nil, EnvOverrides{}, fmt.Errorf("config line %d: missing '='", ln+1)
		}
		key := strings.TrimSpace(t[:eq])
		val := strings.TrimSpace(t[eq+1:])
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[0] == val[len(val)-1] {
			val = val[1 : len(val)-1]
		}
		if err := cfg.set(section, key, val); err != nil {
			return nil, EnvOverrides{}, fmt.Errorf("config line %d: %w", ln+1, err)
		}
	}

	ovr, err := cfg.applyEnv()
	if err != nil {
		return nil, ovr, err
	}
	return cfg, ovr, nil
}

func (c *Config) set(section, key, val string) error {
	switch section {
	case "http":
		if key == "port" {
			var n int
			_, _ = fmt.Sscanf(val, "%d", &n)
			c.HTTP.Port = n
		}
	case "pg":
		if key == "dsn" {
			c.PG.DSN = val
		}
	case "redis":
		switch key {
		case "addr":
			c.Redis.Addr = val
		case "password":
			c.Redis.Password = val
		case "db":
			var n int
			_, _ = fmt.Sscanf(val, "%d", &n)
			c.Redis.DB = n
		}
	case "worker":
		var n int
		_, _ = fmt.Sscanf(val, "%d", &n)
		switch key {
		case "scan_interval_ms":
			c.Worker.ScanInterval = time.Duration(n) * time.Millisecond
		case "batch_per_tick":
			c.Worker.BatchPerTick = n
		case "max_attempts":
			c.Worker.MaxAttempts = n
		case "base_delay_ms":
			c.Worker.BaseDelay = time.Duration(n) * time.Millisecond
		case "max_delay_ms":
			c.Worker.MaxDelay = time.Duration(n) * time.Millisecond
		case "delivered_retain_hours":
			c.Worker.DeliveredRetention = time.Duration(n) * time.Hour
		case "dead_letter_retain_hours":
			c.Worker.DeadLetterRetain = time.Duration(n) * time.Hour
		case "janitor_interval_minutes":
			// 0 is a meaningful value here: it turns retention off.
			c.Worker.JanitorInterval = time.Duration(n) * time.Minute
		case "janitor_batch":
			c.Worker.JanitorBatch = n
		case "janitor_max_batches":
			c.Worker.JanitorMaxBatches = n
		}
	case "dedicated_provider_validation":
		switch key {
		case "provider_id_format":
			mode, err := parseTriStateMode(val)
			if err != nil {
				return fmt.Errorf("dedicated_provider_validation.%s: %w", key, err)
			}
			c.DedicatedProviderValidation.ProviderIDFormat = mode
		case "provider_exists":
			mode, err := parseTriStateMode(val)
			if err != nil {
				return fmt.Errorf("dedicated_provider_validation.%s: %w", key, err)
			}
			c.DedicatedProviderValidation.ProviderExists = mode
		case "provider_dedicated_access":
			mode, err := parseTriStateMode(val)
			if err != nil {
				return fmt.Errorf("dedicated_provider_validation.%s: %w", key, err)
			}
			c.DedicatedProviderValidation.ProviderDedicatedAccess = mode
		case "provider_protocol_binding":
			mode, err := parseTriStateMode(val)
			if err != nil {
				return fmt.Errorf("dedicated_provider_validation.%s: %w", key, err)
			}
			c.DedicatedProviderValidation.ProviderProtocolBinding = mode
		case "provider_owner_scope_matches":
			mode, err := parseTriStateMode(val)
			if err != nil {
				return fmt.Errorf("dedicated_provider_validation.%s: %w", key, err)
			}
			c.DedicatedProviderValidation.ProviderOwnerScopeMatches = mode
		}
	case "key_admission":
		if key == "strict" {
			strict, err := strconv.ParseBool(val)
			if err != nil {
				return fmt.Errorf("key_admission.strict must be true or false: %w", err)
			}
			c.KeyAdmission.Strict = strict
		}
	default:
		if name, ok := strings.CutPrefix(section, "caller:"); ok && name != "" {
			caller := c.Callers[name]
			switch key {
			case "namespace":
				caller.Namespace = val
			case "tokens":
				caller.Tokens = splitCSV(val)
			}
			c.Callers[name] = caller
		}
	}
	return nil
}

func parseTriStateMode(v string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(v))
	switch mode {
	case "off", "warn", "enforce":
		return mode, nil
	default:
		return "", fmt.Errorf("mode %q must be one of off, warn, enforce", v)
	}
}

// splitCSV trims surrounding whitespace from each comma-separated value and
// drops empty entries — so "a, b ,, c" → ["a","b","c"].
func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// applyEnv applies env-var overrides on top of file-loaded config.
// Every parse failure returns a wrapped error so Load propagates it to
// main and publisher fail-fasts at startup instead of booting with a
// half-applied config and surfacing the problem hours later as opaque
// runtime errors. Returned EnvOverrides lists exactly which env vars
// were applied, so main can emit one structured log per var.
func (c *Config) applyEnv() (EnvOverrides, error) {
	var ovr EnvOverrides

	if v := os.Getenv("HTTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return ovr, fmt.Errorf("parse HTTP_PORT env %q: %w", v, err)
		}
		if n <= 0 {
			return ovr, fmt.Errorf("HTTP_PORT env %q must be > 0", v)
		}
		c.HTTP.Port = n
		ovr.HTTPPort = n
		ovr.HTTPPortApplied = true
	}
	if v := os.Getenv("PG_DSN"); v != "" {
		c.PG.DSN = v
		ovr.PGDSNApplied = true
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		c.Redis.Addr = v
		ovr.RedisAddr = v
		ovr.RedisAddrApplied = true
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		c.Redis.Password = v
		ovr.RedisPasswordApplied = true
	}
	if v := os.Getenv("PUBLISHER_KEY_ADMISSION_STRICT"); v != "" {
		strict, err := strconv.ParseBool(v)
		if err != nil {
			return ovr, fmt.Errorf("parse PUBLISHER_KEY_ADMISSION_STRICT env %q: %w", v, err)
		}
		c.KeyAdmission.Strict = strict
		ovr.KeyAdmissionStrict = strict
		ovr.KeyAdmissionStrictApplied = true
	}
	// Retention knobs. Production deployments mount publisher-config as
	// environment only and ship no cfg file, so without these the janitor
	// could not be tuned or switched off in a running environment.
	// PUBLISHER_JANITOR_INTERVAL_MINUTES=0 disables retention.
	if v := os.Getenv("PUBLISHER_JANITOR_INTERVAL_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return ovr, fmt.Errorf("parse PUBLISHER_JANITOR_INTERVAL_MINUTES env %q: want a non-negative integer", v)
		}
		c.Worker.JanitorInterval = time.Duration(n) * time.Minute
		ovr.JanitorInterval = c.Worker.JanitorInterval
		ovr.JanitorIntervalApplied = true
	}
	if v := os.Getenv("PUBLISHER_JANITOR_BATCH"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ovr, fmt.Errorf("parse PUBLISHER_JANITOR_BATCH env %q: want a positive integer", v)
		}
		c.Worker.JanitorBatch = n
		ovr.JanitorBoundsApplied = true
	}
	if v := os.Getenv("PUBLISHER_JANITOR_MAX_BATCHES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ovr, fmt.Errorf("parse PUBLISHER_JANITOR_MAX_BATCHES env %q: want a positive integer", v)
		}
		c.Worker.JanitorMaxBatches = n
		ovr.JanitorBoundsApplied = true
	}
	if v := os.Getenv("PUBLISHER_DELIVERED_RETAIN_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ovr, fmt.Errorf("parse PUBLISHER_DELIVERED_RETAIN_HOURS env %q: want a positive integer", v)
		}
		c.Worker.DeliveredRetention = time.Duration(n) * time.Hour
		ovr.RetentionWindowsApplied = true
	}
	if v := os.Getenv("PUBLISHER_DEAD_LETTER_RETAIN_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ovr, fmt.Errorf("parse PUBLISHER_DEAD_LETTER_RETAIN_HOURS env %q: want a positive integer", v)
		}
		c.Worker.DeadLetterRetain = time.Duration(n) * time.Hour
		ovr.RetentionWindowsApplied = true
	}
	// CALLERS_JSON overrides the [caller:*] sections. Shape:
	//   {"user-service":    {"namespace": "user",    "tokens": ["tok_a","tok_b"]},
	//    "billing-service": {"namespace": "billing", "tokens": ["tok_c"]}}
	// Merge semantics — env entries replace per-service, file entries kept
	// for services env didn't mention. Useful for k8s Secret / Vault
	// injection where the cfg file is baked into the image.
	if v := os.Getenv("CALLERS_JSON"); v != "" {
		var m map[string]Caller
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return ovr, fmt.Errorf("parse CALLERS_JSON env: %w", err)
		}
		services := make([]string, 0, len(m))
		for svc, c2 := range m {
			c.Callers[svc] = c2
			services = append(services, svc)
		}
		sort.Strings(services)
		ovr.CallersServices = services
	}
	return ovr, nil
}
