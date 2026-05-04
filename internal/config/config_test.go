package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAccountIdentifierRequiresEmailOrMobile(t *testing.T) {
	acc := Account{Token: "example-token-value"}
	id := acc.Identifier()
	if id != "" {
		t.Fatalf("expected empty identifier when only token is present, got %q", id)
	}
}

func TestAccountIdentifierTreatsLegacyMobileEmailAsEmail(t *testing.T) {
	acc := Account{Mobile: " user@example.com ", Password: "p"}
	if got := acc.Identifier(); got != "user@example.com" {
		t.Fatalf("expected email identifier from legacy mobile field, got %q", got)
	}
	normalized := NormalizeAccountIdentity(acc)
	if normalized.Email != "user@example.com" || normalized.Mobile != "" {
		t.Fatalf("expected email to be moved out of mobile, got %#v", normalized)
	}
}

func TestLoadStoreNormalizesLegacyMobileEmailAccounts(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"mobile":"legacy@example.com","password":"p","token":"runtime-token"}]
	}`)

	store := LoadStore()
	accounts := store.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("expected legacy mobile email account to survive loading, got %d", len(accounts))
	}
	if accounts[0].Email != "legacy@example.com" || accounts[0].Mobile != "" {
		t.Fatalf("expected legacy mobile email normalized to email, got %#v", accounts[0])
	}
	if accounts[0].Token != "" {
		t.Fatalf("expected config token to be cleared after loading, got %q", accounts[0].Token)
	}
}

func TestLoadStoreClearsTokensFromConfigInput(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[{"email":"u@example.com","password":"p","token":"token-only-account"}]
	}`)

	store := LoadStore()
	accounts := store.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].Token != "" {
		t.Fatalf("expected token to be cleared after loading, got %q", accounts[0].Token)
	}
}

func TestLoadStorePreservesProxiesAndAccountProxyAssignment(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"proxies":[
			{
				"id":"proxy-sh-1",
				"name":"Shanghai Exit",
				"type":"socks5h",
				"host":"127.0.0.1",
				"port":1080,
				"username":"demo",
				"password":"secret"
			}
		],
		"accounts":[
			{
				"email":"u@example.com",
				"password":"p",
				"proxy_id":"proxy-sh-1"
			}
		]
	}`)

	store := LoadStore()
	snap := store.Snapshot()
	if len(snap.Proxies) != 1 {
		t.Fatalf("expected 1 proxy, got %d", len(snap.Proxies))
	}
	if snap.Proxies[0].ID != "proxy-sh-1" {
		t.Fatalf("unexpected proxy id: %#v", snap.Proxies[0])
	}
	if snap.Proxies[0].Type != "socks5h" {
		t.Fatalf("unexpected proxy type: %#v", snap.Proxies[0])
	}
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(snap.Accounts))
	}
	if snap.Accounts[0].ProxyID != "proxy-sh-1" {
		t.Fatalf("expected account proxy assignment preserved, got %#v", snap.Accounts[0])
	}
}

func TestLoadStoreDropsLegacyTokenOnlyAccounts(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[
			{"token":"legacy-token-only"},
			{"email":"u@example.com","password":"p","token":"runtime-token"}
		]
	}`)

	store := LoadStore()
	accounts := store.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("expected token-only account to be dropped, got %d accounts", len(accounts))
	}
	if accounts[0].Identifier() != "u@example.com" {
		t.Fatalf("unexpected remaining account: %#v", accounts[0])
	}
	if accounts[0].Token != "" {
		t.Fatalf("expected persisted token to be cleared, got %q", accounts[0].Token)
	}
}

func TestLoadStorePreservesFileBackedTokensForRuntime(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	defer func() { _ = tmp.Close() }()
	if _, err := tmp.WriteString(`{
		"accounts":[{"email":"u@example.com","password":"p","token":"persisted-token"}]
	}`); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", tmp.Name())

	store := LoadStore()
	accounts := store.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].Token != "persisted-token" {
		t.Fatalf("expected file-backed token preserved for runtime use, got %q", accounts[0].Token)
	}
}

func TestLoadStoreIgnoresLegacyConfigJSONEnv(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "")
	t.Setenv("CONFIG_JSON", `{"keys":["legacy-key"],"accounts":[{"email":"legacy@example.com","password":"p"}]}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", path)

	store := LoadStore()
	if store.HasEnvConfigSource() {
		t.Fatal("expected legacy CONFIG_JSON to be ignored")
	}
	if store.IsEnvBacked() {
		t.Fatal("expected store to remain file-backed/empty when only CONFIG_JSON is set")
	}
	if len(store.Keys()) != 0 || len(store.Accounts()) != 0 {
		t.Fatalf("expected ignored legacy env to leave store empty, got keys=%d accounts=%d", len(store.Keys()), len(store.Accounts()))
	}
}

func TestEnvBackedStoreWritebackBootstrapsMissingConfigFile(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":["k1"],"accounts":[{"email":"seed@example.com","password":"p"}]}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", path)
	t.Setenv("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH", filepath.Join(filepath.Dir(path), "accounts.sqlite"))
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "1")

	store := LoadStore()
	defer func() { _ = store.Close() }()
	if store.IsEnvBacked() {
		t.Fatalf("expected writeback bootstrap to become file-backed immediately")
	}
	if err := store.Update(func(c *Config) error {
		c.Accounts = append(c.Accounts, Account{Email: "new@example.com", Password: "p2"})
		return nil
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if strings.Contains(string(content), `"accounts"`) {
		t.Fatalf("expected accounts to be stored in sqlite instead of config json, got: %s", content)
	}

	reloaded := LoadStore()
	defer func() { _ = reloaded.Close() }()
	if reloaded.IsEnvBacked() {
		t.Fatalf("expected reloaded store to prefer persisted config file")
	}
	accounts := reloaded.Accounts()
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts after reload, got %d", len(accounts))
	}
}

func TestEnvBackedStoreWritebackDoesNotBootstrapOnInvalidEnvJSON(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "{invalid-json")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", path)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "1")

	cfg, fromEnv, loadErr := loadConfig()
	if loadErr == nil {
		t.Fatalf("expected loadConfig error for invalid env json")
	}
	if !fromEnv {
		t.Fatalf("expected fromEnv=true when parsing env config fails")
	}
	if len(cfg.Keys) != 0 || len(cfg.Accounts) != 0 {
		t.Fatalf("expected empty config on parse failure, got keys=%d accounts=%d", len(cfg.Keys), len(cfg.Accounts))
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no bootstrapped config file, stat err=%v", statErr)
	}
}

func TestEnvBackedStoreWritebackDoesNotBootstrapOnInvalidSemanticConfig(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[{"email":"seed@example.com","password":"p"}],
		"runtime":{"account_max_inflight":300}
	}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", path)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "1")

	cfg, fromEnv, loadErr := loadConfig()
	if loadErr == nil {
		t.Fatalf("expected loadConfig error for invalid runtime config")
	}
	if !fromEnv {
		t.Fatalf("expected fromEnv=true when env config is the source")
	}
	if !strings.Contains(loadErr.Error(), "runtime.account_max_inflight") {
		t.Fatalf("expected runtime validation error, got %v", loadErr)
	}
	if len(cfg.Keys) != 1 || len(cfg.Accounts) != 1 {
		t.Fatalf("expected env config to be parsed before validation, got keys=%d accounts=%d", len(cfg.Keys), len(cfg.Accounts))
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected invalid config not to be bootstrapped, stat err=%v", statErr)
	}
}

func TestLoadStoreWithErrorRejectsInvalidRuntimeConfig(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[{"email":"u@example.com","password":"p"}],
		"runtime":{"account_max_inflight":300}
	}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "0")

	if _, err := LoadStoreWithError(); err == nil {
		t.Fatal("expected LoadStoreWithError to reject invalid runtime config")
	} else if !strings.Contains(err.Error(), "runtime.account_max_inflight") {
		t.Fatalf("expected runtime validation error, got %v", err)
	}
}

func TestEnvBackedStoreWritebackFallsBackToPersistedFileOnInvalidEnvJSON(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	path := tmp.Name()
	if _, err := tmp.WriteString(`{"keys":["file-key"],"accounts":[{"email":"persisted@example.com","password":"p"}]}`); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	_ = tmp.Close()

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "{invalid-json")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", path)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "1")

	cfg, fromEnv, loadErr := loadConfig()
	if loadErr != nil {
		t.Fatalf("expected fallback to persisted file, got error: %v", loadErr)
	}
	if fromEnv {
		t.Fatalf("expected fallback to file-backed mode")
	}
	if len(cfg.Keys) != 1 || cfg.Keys[0] != "file-key" {
		t.Fatalf("unexpected keys after fallback: %#v", cfg.Keys)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Email != "persisted@example.com" {
		t.Fatalf("unexpected accounts after fallback: %#v", cfg.Accounts)
	}
}

func TestRuntimeTokenRefreshIntervalHoursDefaultsToSix(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[{"email":"u@example.com","password":"p"}]
	}`)

	store := LoadStore()
	if got := store.RuntimeTokenRefreshIntervalHours(); got != 6 {
		t.Fatalf("expected default refresh interval 6, got %d", got)
	}
}

func TestRuntimeTokenRefreshIntervalHoursUsesConfigValue(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[{"email":"u@example.com","password":"p"}],
		"runtime":{"token_refresh_interval_hours":9}
	}`)

	store := LoadStore()
	if got := store.RuntimeTokenRefreshIntervalHours(); got != 9 {
		t.Fatalf("expected configured refresh interval 9, got %d", got)
	}
}

func TestStoreUpdateAccountTokenKeepsIdentifierResolvable(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"user@example.com","password":"p"}]
	}`)

	store := LoadStore()
	before := store.Accounts()
	if len(before) != 1 {
		t.Fatalf("expected 1 account, got %d", len(before))
	}
	oldID := before[0].Identifier()
	if err := store.UpdateAccountToken(oldID, "new-token"); err != nil {
		t.Fatalf("update token failed: %v", err)
	}

	if got, ok := store.FindAccount(oldID); !ok || got.Token != "new-token" {
		t.Fatalf("expected find by stable account identifier")
	}
}

func TestLoadStoreRejectsInvalidFieldType(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":"not-array","accounts":[]}`)
	store := LoadStore()
	if len(store.Keys()) != 0 || len(store.Accounts()) != 0 {
		t.Fatalf("expected empty store when config type is invalid")
	}
}

func TestParseConfigStringSupportsQuotedBase64Prefix(t *testing.T) {
	rawJSON := `{"keys":["k1"],"accounts":[{"email":"u@example.com","password":"p"}]}`
	b64 := base64.StdEncoding.EncodeToString([]byte(rawJSON))
	cfg, err := parseConfigString(`"base64:` + b64 + `"`)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(cfg.Keys) != 1 || cfg.Keys[0] != "k1" {
		t.Fatalf("unexpected keys: %#v", cfg.Keys)
	}
}

func TestParseConfigStringSupportsRawURLBase64(t *testing.T) {
	rawJSON := `{"keys":["k-url"],"accounts":[]}`
	b64 := base64.RawURLEncoding.EncodeToString([]byte(rawJSON))
	cfg, err := parseConfigString(b64)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(cfg.Keys) != 1 || cfg.Keys[0] != "k-url" {
		t.Fatalf("unexpected keys: %#v", cfg.Keys)
	}
}

func TestStoreUsesUnifiedRuntimeConfigFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	chatPath := filepath.Join(dir, "history.json")
	chatSQLitePath := filepath.Join(dir, "history.sqlite")
	cacheDir := filepath.Join(dir, "response-cache")
	staticDir := filepath.Join(dir, "admin")
	rawRoot := filepath.Join(dir, "samples")
	body := `{
		"server":{
			"port":"7777",
			"bind_addr":"127.0.0.1",
			"log_level":"DEBUG",
			"static_admin_dir":` + quoteJSON(staticDir) + `,
			"auto_build_webui":false,
			"http_total_timeout_seconds":1800
		},
		"admin":{
			"key":"config-admin",
			"jwt_secret":"config-jwt"
		},
		"storage":{
			"chat_history_path":` + quoteJSON(chatPath) + `,
			"chat_history_sqlite_path":` + quoteJSON(chatSQLitePath) + `,
			"raw_stream_sample_root":` + quoteJSON(rawRoot) + `
		},
		"cache":{
			"response":{
				"dir":` + quoteJSON(cacheDir) + `,
				"memory_ttl_seconds":300,
				"disk_ttl_seconds":14400,
				"max_body_bytes":1048576,
				"memory_max_bytes":3800000000,
				"disk_max_bytes":16000000000
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", configPath)

	store := LoadStore()
	if got := store.ServerPort(); got != "7777" {
		t.Fatalf("unexpected port: %q", got)
	}
	if got := store.ServerBindAddr(); got != "127.0.0.1" {
		t.Fatalf("unexpected bind addr: %q", got)
	}
	if got := store.ServerLogLevel(); got != "DEBUG" {
		t.Fatalf("unexpected log level: %q", got)
	}
	if store.ServerAutoBuildWebUI() {
		t.Fatalf("expected auto build disabled from config")
	}
	if got := store.HTTPTotalTimeout(); got != 1800*time.Second {
		t.Fatalf("unexpected HTTP timeout: %s", got)
	}
	if got := store.AdminKey(); got != "config-admin" {
		t.Fatalf("unexpected admin key: %q", got)
	}
	if got := store.AdminJWTSecret(); got != "config-jwt" {
		t.Fatalf("unexpected jwt secret: %q", got)
	}
	if got := store.ChatHistoryPath(); got != chatPath {
		t.Fatalf("unexpected chat history path: %q", got)
	}
	if got := store.ChatHistorySQLitePath(); got != chatSQLitePath {
		t.Fatalf("unexpected chat history sqlite path: %q", got)
	}
	if got := store.RawStreamSampleRoot(); got != rawRoot {
		t.Fatalf("unexpected raw sample root: %q", got)
	}
	if got := store.StaticAdminDir(); got != staticDir {
		t.Fatalf("unexpected static dir: %q", got)
	}
	if got := store.ResponseCacheDir(); got != cacheDir {
		t.Fatalf("unexpected cache dir: %q", got)
	}
	if got := store.ResponseCacheDiskMaxBytes(); got != 16000000000 {
		t.Fatalf("unexpected disk cache cap: %d", got)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAccountTestStatusIsRuntimeOnlyAndNotPersisted(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	defer func() { _ = tmp.Close() }()
	if _, err := tmp.WriteString(`{
		"accounts":[{"email":"u@example.com","password":"p","test_status":"ok"}]
	}`); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", tmp.Name())

	store := LoadStore()
	if got, ok := store.AccountTestStatus("u@example.com"); ok || got != "" {
		t.Fatalf("expected no runtime status loaded from config, got %q", got)
	}
	if err := store.UpdateAccountTestStatus("u@example.com", "ok"); err != nil {
		t.Fatalf("update test status: %v", err)
	}
	if got, ok := store.AccountTestStatus("u@example.com"); !ok || got != "ok" {
		t.Fatalf("expected runtime status to be available, got %q (ok=%v)", got, ok)
	}

	content, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(content), "test_status") {
		t.Fatalf("expected test_status to stay out of persisted config, got: %s", content)
	}
}

func TestAccountSessionCountIsRuntimeOnlyAndNotPersisted(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	defer func() { _ = tmp.Close() }()
	if _, err := tmp.WriteString(`{
		"accounts":[{"email":"u@example.com","password":"p"}]
	}`); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", tmp.Name())

	store := LoadStore()
	if got, ok := store.AccountSessionCount("u@example.com"); ok || got != 0 {
		t.Fatalf("expected no runtime session count loaded from config, got %d (ok=%v)", got, ok)
	}
	if err := store.UpdateAccountSessionCount("u@example.com", 12); err != nil {
		t.Fatalf("update session count: %v", err)
	}
	if got, ok := store.AccountSessionCount("u@example.com"); !ok || got != 12 {
		t.Fatalf("expected runtime session count 12, got %d (ok=%v)", got, ok)
	}

	content, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(content), "session_count") {
		t.Fatalf("expected session_count to stay out of persisted config, got: %s", content)
	}
}
