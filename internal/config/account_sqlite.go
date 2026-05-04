package config

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type accountSQLiteStore struct {
	db   *sql.DB
	path string
}

func accountSQLitePathForConfig(cfg Config, fromEnv bool) string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH")); raw != "" {
		return resolvePathValue(raw, "data/accounts.sqlite")
	}
	if raw := strings.TrimSpace(cfg.Storage.AccountsSQLitePath); raw != "" {
		return resolvePathValue(raw, "data/accounts.sqlite")
	}
	if fromEnv {
		return ""
	}
	if isGoTestBinary() {
		return ""
	}
	if dataDir := strings.TrimSpace(cfg.Storage.DataDir); dataDir != "" {
		return filepath.Join(resolvePathValue(dataDir, "data"), "accounts.sqlite")
	}
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH")); raw != "" {
		configPath := resolvePathValue(raw, "config.json")
		return filepath.Join(filepath.Dir(configPath), "accounts.sqlite")
	}
	return AccountsSQLitePath()
}

func isGoTestBinary() bool {
	name := filepath.Base(os.Args[0])
	return strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".test.exe")
}

func newAccountSQLiteStore(path string, seed []Account) (*accountSQLiteStore, []Account, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, normalizeAndDedupeAccountConfig(seed), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return nil, nil, fmt.Errorf("create accounts sqlite dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("open accounts sqlite: %w", err)
	}
	store := &accountSQLiteStore{db: db, path: path}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	accounts, err := store.list()
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	if len(accounts) == 0 && len(seed) > 0 {
		accounts, err = store.replace(seed)
		if err != nil {
			_ = db.Close()
			return nil, nil, err
		}
	}
	return store, accounts, nil
}

func (s *accountSQLiteStore) init() error {
	if s == nil || s.db == nil {
		return errors.New("accounts sqlite store is nil")
	}
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS accounts (
			identifier TEXT PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			mobile TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			remark TEXT NOT NULL DEFAULT '',
			password TEXT NOT NULL DEFAULT '',
			token TEXT NOT NULL DEFAULT '',
			proxy_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_email ON accounts(email) WHERE email <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_mobile ON accounts(mobile) WHERE mobile <> ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init accounts sqlite: %w", err)
		}
	}
	return nil
}

func (s *accountSQLiteStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *accountSQLiteStore) list() ([]Account, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT name, remark, email, mobile, password, token, proxy_id FROM accounts ORDER BY rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("list accounts sqlite: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var accounts []Account
	for rows.Next() {
		var acc Account
		if err := rows.Scan(&acc.Name, &acc.Remark, &acc.Email, &acc.Mobile, &acc.Password, &acc.Token, &acc.ProxyID); err != nil {
			return nil, fmt.Errorf("scan accounts sqlite: %w", err)
		}
		accounts = append(accounts, normalizeAccountConfig(acc))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan accounts sqlite rows: %w", err)
	}
	return normalizeAndDedupeAccountConfig(accounts), nil
}

func (s *accountSQLiteStore) replace(accounts []Account) ([]Account, error) {
	if s == nil || s.db == nil {
		return normalizeAndDedupeAccountConfig(accounts), nil
	}
	accounts = normalizeAndDedupeAccountConfig(accounts)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin replace accounts sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM accounts`); err != nil {
		return nil, fmt.Errorf("clear accounts sqlite: %w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO accounts(identifier, email, mobile, name, remark, password, token, proxy_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare accounts sqlite insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	now := time.Now().UnixMilli()
	for _, acc := range accounts {
		if _, err := stmt.Exec(acc.Identifier(), acc.Email, acc.Mobile, acc.Name, acc.Remark, acc.Password, acc.Token, acc.ProxyID, now, now); err != nil {
			return nil, fmt.Errorf("insert account %q sqlite: %w", acc.Identifier(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit replace accounts sqlite: %w", err)
	}
	return accounts, nil
}

func (s *accountSQLiteStore) updateToken(identifier, token string) error {
	if s == nil || s.db == nil {
		return nil
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return errors.New("account identifier is required")
	}
	mobile := CanonicalMobileKey(identifier)
	res, err := s.db.Exec(
		`UPDATE accounts SET token = ?, updated_at = ? WHERE identifier = ? OR email = ? OR mobile = ?`,
		strings.TrimSpace(token),
		time.Now().UnixMilli(),
		identifier,
		identifier,
		mobile,
	)
	if err != nil {
		return fmt.Errorf("update account token sqlite: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read account token sqlite affected rows: %w", err)
	}
	if n == 0 {
		return errors.New("account not found")
	}
	return nil
}

func normalizeAndDedupeAccountConfig(accounts []Account) []Account {
	if len(accounts) == 0 {
		return nil
	}
	out := make([]Account, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, acc := range accounts {
		acc = normalizeAccountConfig(acc)
		key := accountConfigDedupeKey(acc)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, acc)
	}
	return out
}

func normalizeAccountConfig(acc Account) Account {
	acc = NormalizeAccountIdentity(acc)
	acc.Name = strings.TrimSpace(acc.Name)
	acc.Remark = strings.TrimSpace(acc.Remark)
	acc.Password = strings.TrimSpace(acc.Password)
	acc.Token = strings.TrimSpace(acc.Token)
	acc.ProxyID = strings.TrimSpace(acc.ProxyID)
	return acc
}

func accountConfigDedupeKey(acc Account) string {
	if email := strings.TrimSpace(acc.Email); email != "" {
		return "email:" + email
	}
	if mobile := CanonicalMobileKey(acc.Mobile); mobile != "" {
		return "mobile:" + mobile
	}
	return ""
}
