package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCfg dumps a minimal valid cfg to a temp file for the env-override
// tests below — each test injects only the field it cares about via env.
func writeCfg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "publisher.cfg")
	if err := os.WriteFile(p, []byte("[http]\nport = 80\n[pg]\ndsn = postgres://x/y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// resetEnvVars clears every CONFIG-related env var so the parent shell's
// state can't leak into a test case. Each test starts from a clean slate
// and uses t.Setenv only for the specific vars it cares about.
func resetEnvVars(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PORT", "PG_DSN", "REDIS_ADDR", "REDIS_PASSWORD", "CALLERS_JSON", "PUBLISHER_KEY_ADMISSION_STRICT"} {
		t.Setenv(k, "")
	}
}

func TestLoad_KeyAdmissionStrictDefaultsFalseAndEnvCanEnable(t *testing.T) {
	resetEnvVars(t)
	cfg, ovr, err := Load(writeCfg(t))
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if cfg.KeyAdmission.Strict || ovr.KeyAdmissionStrictApplied {
		t.Fatalf("strict mode must default compatibility-off: cfg=%+v ovr=%+v", cfg.KeyAdmission, ovr)
	}

	t.Setenv("PUBLISHER_KEY_ADMISSION_STRICT", "true")
	cfg, ovr, err = Load(writeCfg(t))
	if err != nil {
		t.Fatalf("Load env strict: %v", err)
	}
	if !cfg.KeyAdmission.Strict || !ovr.KeyAdmissionStrictApplied || !ovr.KeyAdmissionStrict {
		t.Fatalf("strict env not applied: cfg=%+v ovr=%+v", cfg.KeyAdmission, ovr)
	}
}

func TestLoad_KeyAdmissionStrictRejectsInvalidEnv(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("PUBLISHER_KEY_ADMISSION_STRICT", "sometimes")
	_, _, err := Load(writeCfg(t))
	if err == nil || !strings.Contains(err.Error(), "PUBLISHER_KEY_ADMISSION_STRICT") {
		t.Fatalf("invalid strict env error=%v", err)
	}
}

// TestLoad_CallersJSON_Malformed pins the regression fix for the silent
// fail-on-bad-env path that used to let publisher boot with zero callers
// and 401 every request with nothing in the logs.
func TestLoad_CallersJSON_Malformed(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("CALLERS_JSON", `{"user-service":{not valid json`)
	_, _, err := Load(writeCfg(t))
	if err == nil {
		t.Fatal("expected Load to fail on malformed CALLERS_JSON, got nil")
	}
	if !strings.Contains(err.Error(), "CALLERS_JSON") {
		t.Errorf("error %q should reference CALLERS_JSON so operators know where to look", err.Error())
	}
}

// TestLoad_CallersJSON_Applied asserts the happy-path override still
// merges into cfg.Callers post fail-fast change, AND surfaces the
// service list via EnvOverrides for main's per-var log.
func TestLoad_CallersJSON_Applied(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("CALLERS_JSON", `{"user-service":{"namespace":"user","tokens":["t1","t2"]},"admin-service":{"namespace":"admin","tokens":["t3"]}}`)
	cfg, ovr, err := Load(writeCfg(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c, ok := cfg.Callers["user-service"]
	if !ok {
		t.Fatal("expected user-service in cfg.Callers after CALLERS_JSON override")
	}
	if c.Namespace != "user" {
		t.Errorf("namespace = %q, want user", c.Namespace)
	}
	if len(c.Tokens) != 2 || c.Tokens[0] != "t1" || c.Tokens[1] != "t2" {
		t.Errorf("tokens = %v, want [t1 t2]", c.Tokens)
	}
	// EnvOverrides should list both services in sorted order (so the
	// log line is stable across restarts — map range otherwise).
	want := []string{"admin-service", "user-service"}
	if got := ovr.CallersServices; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ovr.CallersServices = %v, want %v", got, want)
	}
}

// TestLoad_CallersJSON_Unset asserts no env var is a no-op (not an
// error). Guards against accidentally requiring the env on operators
// who configure via the cfg file.
func TestLoad_CallersJSON_Unset(t *testing.T) {
	resetEnvVars(t)
	cfg, ovr, err := Load(writeCfg(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Port != 80 {
		t.Errorf("port = %d, want 80 (cfg-file value, no override)", cfg.HTTP.Port)
	}
	if ovr.CallersServices != nil {
		t.Errorf("ovr.CallersServices = %v, want nil when env unset", ovr.CallersServices)
	}
	if cfg.DedicatedProviderValidation.ProviderIDFormat != "enforce" ||
		cfg.DedicatedProviderValidation.ProviderExists != "enforce" ||
		cfg.DedicatedProviderValidation.ProviderDedicatedAccess != "enforce" ||
		cfg.DedicatedProviderValidation.ProviderProtocolBinding != "enforce" ||
		cfg.DedicatedProviderValidation.ProviderOwnerScopeMatches != "enforce" {
		t.Fatalf("dedicated provider validation defaults must all be enforce, got %+v", cfg.DedicatedProviderValidation)
	}
}

// TestLoad_HTTPPort_Typo pins the new fail-fast behavior for non-numeric
// HTTP_PORT values. Old code silently dropped the parse error and kept
// the cfg-file value, producing "I set 8080a but publisher listens on
// 80" type confusion.
func TestLoad_HTTPPort_Typo(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("HTTP_PORT", "8080a")
	_, _, err := Load(writeCfg(t))
	if err == nil {
		t.Fatal("expected Load to fail on non-numeric HTTP_PORT, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP_PORT") {
		t.Errorf("error %q should reference HTTP_PORT", err.Error())
	}
}

// TestLoad_HTTPPort_Zero pins fail-fast on HTTP_PORT=0 (Go's "any free
// port" — never what an operator intends in a server config).
func TestLoad_HTTPPort_Zero(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("HTTP_PORT", "0")
	_, _, err := Load(writeCfg(t))
	if err == nil {
		t.Fatal("expected Load to fail on HTTP_PORT=0, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP_PORT") {
		t.Errorf("error %q should reference HTTP_PORT", err.Error())
	}
}

// TestLoad_HTTPPort_Applied asserts the happy-path override surfaces
// the new port in both cfg.HTTP.Port and EnvOverrides.
func TestLoad_HTTPPort_Applied(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("HTTP_PORT", "8081")
	cfg, ovr, err := Load(writeCfg(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Port != 8081 {
		t.Errorf("cfg.HTTP.Port = %d, want 8081", cfg.HTTP.Port)
	}
	if !ovr.HTTPPortApplied || ovr.HTTPPort != 8081 {
		t.Errorf("ovr = %+v, want HTTPPortApplied=true HTTPPort=8081", ovr)
	}
}

func TestLoad_DedicatedProviderValidationModes(t *testing.T) {
	resetEnvVars(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "publisher.cfg")
	body := `[http]
port = 80
[pg]
dsn = postgres://x/y
[dedicated_provider_validation]
provider_id_format = enforce
provider_exists = enforce
provider_dedicated_access = warn
provider_protocol_binding = off
provider_owner_scope_matches = enforce
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.DedicatedProviderValidation
	if got.ProviderIDFormat != "enforce" ||
		got.ProviderExists != "enforce" ||
		got.ProviderDedicatedAccess != "warn" ||
		got.ProviderProtocolBinding != "off" ||
		got.ProviderOwnerScopeMatches != "enforce" {
		t.Fatalf("dedicated provider validation modes mismatch: %+v", got)
	}
}

func TestLoad_DedicatedProviderValidationRejectsInvalidMode(t *testing.T) {
	resetEnvVars(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "publisher.cfg")
	body := `[dedicated_provider_validation]
provider_id_format = strict
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected invalid dedicated provider validation mode to fail")
	}
	if !strings.Contains(err.Error(), "provider_id_format") {
		t.Fatalf("error should identify invalid mode key, got %v", err)
	}
}

// TestLoad_EnvOverrides_PGRedis covers the three string-only env vars
// (PG_DSN, REDIS_ADDR, REDIS_PASSWORD) — no parsing involved, but the
// EnvOverrides flags must flip so the startup log can announce them.
func TestLoad_EnvOverrides_PGRedis(t *testing.T) {
	resetEnvVars(t)
	t.Setenv("PG_DSN", "postgres://override/x")
	t.Setenv("REDIS_ADDR", "10.0.0.5:6379")
	t.Setenv("REDIS_PASSWORD", "p")
	cfg, ovr, err := Load(writeCfg(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PG.DSN != "postgres://override/x" {
		t.Errorf("cfg.PG.DSN = %q, want override", cfg.PG.DSN)
	}
	if cfg.Redis.Addr != "10.0.0.5:6379" {
		t.Errorf("cfg.Redis.Addr = %q, want override", cfg.Redis.Addr)
	}
	if cfg.Redis.Password != "p" {
		t.Errorf("cfg.Redis.Password = %q, want override", cfg.Redis.Password)
	}
	if !ovr.PGDSNApplied || !ovr.RedisAddrApplied || !ovr.RedisPasswordApplied {
		t.Errorf("ovr = %+v, want all three Applied=true", ovr)
	}
	if ovr.RedisAddr != "10.0.0.5:6379" {
		t.Errorf("ovr.RedisAddr = %q, want raw value forwarded for caller-side redaction", ovr.RedisAddr)
	}
}

func TestLoad_PublisherCIConfig(t *testing.T) {
	resetEnvVars(t)
	cfg, _, err := Load(filepath.Join("..", "..", "config", "publisher.ci.cfg"))
	if err != nil {
		t.Fatalf("Load publisher.ci.cfg: %v", err)
	}
	if cfg.HTTP.Port != 9080 {
		t.Fatalf("HTTP port=%d want 9080", cfg.HTTP.Port)
	}
	if !strings.Contains(cfg.PG.DSN, "/tokenplatform?") {
		t.Fatalf("PG DSN must include integration database name tokenplatform, got %q", cfg.PG.DSN)
	}
	if cfg.Redis.Addr != "127.0.0.1:6379" {
		t.Fatalf("Redis addr=%q want 127.0.0.1:6379", cfg.Redis.Addr)
	}
	admin := cfg.Callers["admin-service"]
	if admin.Namespace != "admin" ||
		len(admin.Tokens) != 1 ||
		admin.Tokens[0] != "admin_fa5bdf91a711bfeeaa412dcd79dea3af11f78e398f375f20cd024de1f573a002" {
		t.Fatalf("admin-service caller mismatch: %+v", admin)
	}
}
