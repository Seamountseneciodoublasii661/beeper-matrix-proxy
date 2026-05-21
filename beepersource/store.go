package beepersource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db        *sql.DB
	closeOnce sync.Once
	closeErr  error
}

func OpenStore(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		return fmt.Errorf("set sqlite synchronous=NORMAL: %w", err)
	}
	schema := []string{
		`CREATE TABLE IF NOT EXISTS portal (
			beeper_chat_id TEXT PRIMARY KEY,
			matrix_room_id TEXT UNIQUE,
			account_id TEXT,
			last_cursor TEXT,
			last_reconcile_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS puppet (
			beeper_sender_id TEXT PRIMARY KEY,
			matrix_user_id TEXT UNIQUE,
			display_name TEXT,
			avatar_asset_id TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS message_mapping (
			beeper_message_id TEXT PRIMARY KEY,
			matrix_event_id TEXT UNIQUE NOT NULL,
			chat_id TEXT NOT NULL,
			version TEXT,
			deleted_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS message_mapping_chat_idx ON message_mapping(chat_id)`,
		`CREATE TABLE IF NOT EXISTS reaction_mapping (
			beeper_message_id TEXT NOT NULL,
			reaction_key TEXT NOT NULL,
			matrix_event_id TEXT NOT NULL,
			PRIMARY KEY (beeper_message_id, reaction_key)
		)`,
		`CREATE TABLE IF NOT EXISTS pending_mutation (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			beeper_message_id TEXT NOT NULL,
			mutation_type TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS pending_mutation_message_idx ON pending_mutation(beeper_message_id)`,
		`CREATE TABLE IF NOT EXISTS media_cache (
			asset_id TEXT PRIMARY KEY,
			content_hash TEXT,
			matrix_mxc TEXT,
			size INTEGER,
			mime_type TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			direction TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			status TEXT NOT NULL,
			retry_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS outbound_echo (
			chat_id TEXT NOT NULL,
			body TEXT NOT NULL,
			matrix_event_id TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			PRIMARY KEY (chat_id, body, matrix_event_id)
		)`,
		`CREATE INDEX IF NOT EXISTS outbound_echo_lookup_idx ON outbound_echo(chat_id, body, expires_at)`,
	}
	for _, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RememberOutboundEcho(ctx context.Context, chatID string, body string, matrixEventID string, ttl time.Duration) error {
	if chatID == "" || body == "" || matrixEventID == "" {
		return nil
	}
	expiresAt := time.Now().Add(ttl).Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO outbound_echo (chat_id, body, matrix_event_id, expires_at)
		VALUES (?, ?, ?, ?)
	`, chatID, body, matrixEventID, expiresAt)
	return err
}

func (s *Store) ConsumeOutboundEcho(ctx context.Context, chatID string, body string) (string, bool, error) {
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, "DELETE FROM outbound_echo WHERE expires_at < ?", now); err != nil {
		return "", false, err
	}
	var matrixEventID string
	err := s.db.QueryRowContext(ctx, `
		SELECT matrix_event_id FROM outbound_echo
		WHERE chat_id=? AND body=? AND expires_at>=?
		ORDER BY expires_at LIMIT 1
	`, chatID, body, now).Scan(&matrixEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM outbound_echo WHERE chat_id=? AND body=? AND matrix_event_id=?", chatID, body, matrixEventID)
	return matrixEventID, true, err
}

func (s *Store) GetValue(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) SetValue(ctx context.Context, key string, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, key, value)
	return err
}

func (s *Store) UpsertPortal(ctx context.Context, chat Chat, matrixRoomID string, cursor string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO portal (beeper_chat_id, matrix_room_id, account_id, last_cursor, last_reconcile_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(beeper_chat_id) DO UPDATE SET
			matrix_room_id=excluded.matrix_room_id,
			account_id=excluded.account_id,
			last_cursor=excluded.last_cursor,
			last_reconcile_at=excluded.last_reconcile_at
	`, chat.ID, matrixRoomID, chat.AccountID, cursor, time.Now().Unix())
	return err
}

func (s *Store) PortalCursor(ctx context.Context, chatID string) (string, error) {
	var cursor sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT last_cursor FROM portal WHERE beeper_chat_id=?", chatID).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cursor.String, nil
}

func (s *Store) PortalRoomID(ctx context.Context, chatID string) (string, bool, error) {
	var roomID string
	err := s.db.QueryRowContext(ctx, "SELECT matrix_room_id FROM portal WHERE beeper_chat_id=?", chatID).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return roomID, true, nil
}

func (s *Store) PortalChatIDByRoomID(ctx context.Context, matrixRoomID string) (string, bool, error) {
	var chatID string
	err := s.db.QueryRowContext(ctx, "SELECT beeper_chat_id FROM portal WHERE matrix_room_id=?", matrixRoomID).Scan(&chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return chatID, true, nil
}

func (s *Store) UpsertPuppet(ctx context.Context, sender Sender, matrixUserID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO puppet (beeper_sender_id, matrix_user_id, display_name, avatar_asset_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(beeper_sender_id) DO UPDATE SET
			matrix_user_id=excluded.matrix_user_id,
			display_name=excluded.display_name,
			avatar_asset_id=excluded.avatar_asset_id
	`, sender.ID, matrixUserID, sender.DisplayName, sender.AvatarID)
	return err
}

func (s *Store) UpsertMessageMapping(ctx context.Context, mapping MessageMapping) error {
	var deletedAt any
	if mapping.DeletedAt != nil {
		deletedAt = mapping.DeletedAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_mapping (beeper_message_id, matrix_event_id, chat_id, version, deleted_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(beeper_message_id) DO UPDATE SET
			matrix_event_id=excluded.matrix_event_id,
			chat_id=excluded.chat_id,
			version=excluded.version,
			deleted_at=excluded.deleted_at
	`, mapping.BeeperMessageID, mapping.MatrixEventID, mapping.ChatID, mapping.Version, deletedAt)
	return err
}

func (s *Store) MessageByBeeperID(ctx context.Context, beeperMessageID string) (MessageMapping, bool, error) {
	var mapping MessageMapping
	var deletedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT beeper_message_id, matrix_event_id, chat_id, version, deleted_at
		FROM message_mapping WHERE beeper_message_id=?
	`, beeperMessageID).Scan(&mapping.BeeperMessageID, &mapping.MatrixEventID, &mapping.ChatID, &mapping.Version, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageMapping{}, false, nil
	}
	if err != nil {
		return MessageMapping{}, false, err
	}
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0).UTC()
		mapping.DeletedAt = &t
	}
	return mapping, true, nil
}

func (s *Store) EnqueuePendingMutation(ctx context.Context, mutation PendingMutation) (int64, error) {
	createdAt := mutation.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO pending_mutation (beeper_message_id, mutation_type, payload_json, created_at)
		VALUES (?, ?, ?, ?)
	`, mutation.BeeperMessageID, mutation.MutationType, mutation.PayloadJSON, createdAt.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) PendingMutations(ctx context.Context, beeperMessageID string) ([]PendingMutation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, beeper_message_id, mutation_type, payload_json, created_at
		FROM pending_mutation WHERE beeper_message_id=? ORDER BY id
	`, beeperMessageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingMutation
	for rows.Next() {
		var m PendingMutation
		var createdAt int64
		if err := rows.Scan(&m.ID, &m.BeeperMessageID, &m.MutationType, &m.PayloadJSON, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}
