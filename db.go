package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type store struct {
	db  *sql.DB
	key []byte
}

func openStore() (*store, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("APP_KEY"))
	if err != nil || len(key) != 32 {
		return nil, errors.New("APP_KEY must be base64 for exactly 32 random bytes")
	}
	path := os.Getenv("DATABASE_PATH")
	if path == "" {
		path = "data/trafficflow.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db, key: key}
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS devices (
			id INTEGER PRIMARY KEY, name TEXT NOT NULL, address TEXT NOT NULL UNIQUE, os TEXT NOT NULL,
			snmp_version TEXT NOT NULL, credential BLOB NOT NULL, status TEXT NOT NULL DEFAULT 'unknown',
			last_seen INTEGER, uptime INTEGER, last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS interfaces (
			id INTEGER PRIMARY KEY, device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
			if_index INTEGER NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', mac TEXT NOT NULL DEFAULT '',
			speed_bps INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'unknown',
			rx_bps REAL, tx_bps REAL, last_sample INTEGER,
			UNIQUE(device_id, if_index))`,
		`CREATE TABLE IF NOT EXISTS links (
			id INTEGER PRIMARY KEY, a_interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
			b_interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
			source TEXT NOT NULL, UNIQUE(a_interface_id, b_interface_id), CHECK(a_interface_id != b_interface_id))`,
		`CREATE TABLE IF NOT EXISTS candidates (
			id INTEGER PRIMARY KEY, local_interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
			remote_device_id INTEGER REFERENCES devices(id) ON DELETE CASCADE,
			remote_port TEXT NOT NULL, remote_name TEXT NOT NULL,
			last_seen INTEGER NOT NULL, UNIQUE(local_interface_id, remote_name, remote_port))`,
		`CREATE TABLE IF NOT EXISTS suppressed_links (
			a_interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
			b_interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
			PRIMARY KEY(a_interface_id,b_interface_id))`,
		`CREATE TABLE IF NOT EXISTS samples (
			link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE, ts INTEGER NOT NULL,
			rx_bps REAL NOT NULL, tx_bps REAL NOT NULL, PRIMARY KEY(link_id,ts))`,
		`CREATE TABLE IF NOT EXISTS rollups (
			link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE, minute INTEGER NOT NULL,
			rx_bps REAL NOT NULL, tx_bps REAL NOT NULL, count INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(link_id,minute))`,
		`CREATE TABLE IF NOT EXISTS fdb (
			interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE, mac TEXT NOT NULL,
			last_seen INTEGER NOT NULL, PRIMARY KEY(interface_id,mac))`,
		`CREATE TABLE IF NOT EXISTS device_macs (
			device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE, mac TEXT NOT NULL, PRIMARY KEY(device_id,mac))`,
		// Rejected link suggestions. Kept apart from candidates, which are pruned when LLDP stops
		// reporting them, so a rejection survives the suggestion disappearing and coming back.
		`CREATE TABLE IF NOT EXISTS dismissed_candidates (
			id INTEGER PRIMARY KEY, local_interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
			remote_name TEXT NOT NULL, remote_port TEXT NOT NULL, dismissed_at INTEGER NOT NULL,
			UNIQUE(local_interface_id, remote_name, remote_port))`,
		// Encrypted application settings, such as alert channels, keyed by name.
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value BLOB NOT NULL)`,
		// Alarms are open while cleared_at is NULL, so a restart neither repeats nor loses them.
		`CREATE TABLE IF NOT EXISTS alarms (
			id INTEGER PRIMARY KEY, kind TEXT NOT NULL,
			device_id INTEGER REFERENCES devices(id) ON DELETE CASCADE,
			interface_id INTEGER REFERENCES interfaces(id) ON DELETE CASCADE,
			title TEXT NOT NULL, started_at INTEGER NOT NULL, cleared_at INTEGER)`,
		"CREATE INDEX IF NOT EXISTS samples_ts ON samples(ts)",
		"CREATE INDEX IF NOT EXISTS rollups_minute ON rollups(minute)",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("schema: %w", err)
		}
	}
	// Columns added after the first release; re-running them reports a duplicate column.
	for _, statement := range []string{
		"ALTER TABLE devices ADD COLUMN resolved TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE devices ADD COLUMN map_x REAL",
		"ALTER TABLE devices ADD COLUMN map_y REAL",
		"ALTER TABLE interfaces ADD COLUMN alert INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE devices ADD COLUMN kind TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE devices ADD COLUMN location TEXT NOT NULL DEFAULT ''",
		// Interface errors per minute over the latest poll interval; NULL when unknown.
		"ALTER TABLE interfaces ADD COLUMN rx_errors REAL",
		"ALTER TABLE interfaces ADD COLUMN tx_errors REAL",
	} {
		if _, err := db.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	// Externals created before servers existed were all clouds.
	if _, err := db.Exec("UPDATE devices SET kind='cloud' WHERE os='external' AND kind=''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *store) encrypt(plain string) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *store) decrypt(value []byte) (string, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(value) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted credential")
	}
	plain, err := gcm.Open(nil, value[:gcm.NonceSize()], value[gcm.NonceSize():], nil)
	return string(plain), err
}

func (s *store) cleanup(ctx context.Context) error {
	now := time.Now().Unix()
	for _, q := range []struct {
		sql    string
		before int64
	}{
		{"DELETE FROM sessions WHERE expires_at < ?", now},
		{"DELETE FROM samples WHERE ts < ?", now - 86400},
		{"DELETE FROM rollups WHERE minute < ?", now - 30*86400},
		{"DELETE FROM candidates WHERE last_seen < ?", now - 15*60},
		{"DELETE FROM fdb WHERE last_seen < ?", now - 30*60},
		{"DELETE FROM alarms WHERE cleared_at < ?", now - 30*86400},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.before); err != nil {
			return err
		}
	}
	return nil
}
