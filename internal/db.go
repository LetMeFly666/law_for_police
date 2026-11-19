package internal

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func OpenDB(ctx context.Context, c Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", c.MySQLDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(20)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id CHAR(36) PRIMARY KEY,
			recovery_hash CHAR(64) NOT NULL UNIQUE,
			recovery_code_ciphertext TEXT NULL,
			nickname VARCHAR(60) NOT NULL DEFAULT '',
			email VARCHAR(254) NULL UNIQUE,
			email_verified TINYINT(1) NOT NULL DEFAULT 0,
			sync_version BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS devices (
			id CHAR(36) PRIMARY KEY,
			user_id CHAR(36) NOT NULL,
			token_hash CHAR(64) NOT NULL UNIQUE,
			created_at BIGINT NOT NULL,
			last_seen BIGINT NOT NULL,
			KEY idx_devices_user (user_id),
			CONSTRAINT fk_devices_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS user_items (
			user_id CHAR(36) NOT NULL,
			item_key VARCHAR(190) NOT NULL,
			value_json LONGTEXT NOT NULL,
			updated_at BIGINT NOT NULL,
			sync_version BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, item_key),
			KEY idx_user_items_sync (user_id, sync_version),
			CONSTRAINT fk_items_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS experiences (
			id CHAR(36) PRIMARY KEY,
			user_id CHAR(36) NOT NULL,
			question_id VARCHAR(80) NOT NULL,
			nickname VARCHAR(60) NOT NULL,
			content TEXT NOT NULL,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL DEFAULT 0,
			KEY idx_experiences_question (question_id, created_at),
			KEY idx_experiences_user (user_id, created_at),
			CONSTRAINT fk_experiences_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS experience_likes (
			experience_id CHAR(36) NOT NULL,
			user_id CHAR(36) NOT NULL,
			created_at BIGINT NOT NULL,
			PRIMARY KEY (experience_id,user_id),
			KEY idx_experience_likes_user (user_id,created_at),
			CONSTRAINT fk_experience_likes_experience FOREIGN KEY (experience_id) REFERENCES experiences(id) ON DELETE CASCADE,
			CONSTRAINT fk_experience_likes_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS study_time (
			user_id CHAR(36) PRIMARY KEY,
			total_ms BIGINT NOT NULL DEFAULT 0,
			updated_at BIGINT NOT NULL,
			CONSTRAINT fk_study_time_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS study_time_batches (
			user_id CHAR(36) NOT NULL,
			batch_id CHAR(36) NOT NULL,
			delta_ms BIGINT NOT NULL,
			created_at BIGINT NOT NULL,
			PRIMARY KEY (user_id,batch_id),
			KEY idx_study_batches_created (created_at),
			CONSTRAINT fk_study_batches_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS feedback (
			id CHAR(36) PRIMARY KEY,
			user_id CHAR(36) NULL,
			nickname VARCHAR(60) NOT NULL DEFAULT '',
			email VARCHAR(254) NOT NULL DEFAULT '',
			category VARCHAR(40) NOT NULL,
			content TEXT NOT NULL,
			status VARCHAR(20) NOT NULL DEFAULT 'new',
			created_at BIGINT NOT NULL,
			KEY idx_feedback_created (created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS email_codes (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			email VARCHAR(254) NOT NULL,
			purpose VARCHAR(20) NOT NULL,
			user_id CHAR(36) NULL,
			code_hash CHAR(64) NOT NULL,
			attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
			expires_at BIGINT NOT NULL,
			created_at BIGINT NOT NULL,
			KEY idx_email_code_lookup (email, purpose, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("数据库自动建表失败: %w", err)
		}
	}
	// Keep deployments upgrade-safe if the earlier SQLite/MySQL prototype schema
	// already exists. The column names and DDL are fixed constants, not user input.
	for _, column := range []struct{ table, name, ddl string }{
		{"experiences", "updated_at", "ALTER TABLE experiences ADD COLUMN updated_at BIGINT NOT NULL DEFAULT 0"},
		{"users", "sync_version", "ALTER TABLE users ADD COLUMN sync_version BIGINT NOT NULL DEFAULT 0"},
		{"users", "recovery_code_ciphertext", "ALTER TABLE users ADD COLUMN recovery_code_ciphertext TEXT NULL"},
		{"user_items", "sync_version", "ALTER TABLE user_items ADD COLUMN sync_version BIGINT NOT NULL DEFAULT 0"},
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?`, column.table, column.name).Scan(&count); err != nil {
			return fmt.Errorf("检查数据库字段失败: %w", err)
		}
		if count == 0 {
			if _, err := db.ExecContext(ctx, column.ddl); err != nil {
				return fmt.Errorf("升级数据库字段 %s.%s 失败: %w", column.table, column.name, err)
			}
		}
	}
	if err := backfillSyncVersions(ctx, db); err != nil {
		return err
	}
	return nil
}

// backfillSyncVersions assigns unique revisions to rows created by an earlier
// schema version before sync_version existed. It is a no-op on a fresh database.
func backfillSyncVersions(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT user_id FROM user_items WHERE sync_version=0`)
	if err != nil {
		return fmt.Errorf("读取待升级的学习记录失败: %w", err)
	}
	var userIDs []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, userID := range userIDs {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var revision int64
		if err = tx.QueryRowContext(ctx, `SELECT sync_version FROM users WHERE id=? FOR UPDATE`, userID).Scan(&revision); err != nil {
			tx.Rollback()
			return err
		}
		items, err := tx.QueryContext(ctx, `SELECT item_key FROM user_items WHERE user_id=? AND sync_version=0 ORDER BY item_key`, userID)
		if err != nil {
			tx.Rollback()
			return err
		}
		var keys []string
		for items.Next() {
			var key string
			if err = items.Scan(&key); err != nil {
				items.Close()
				tx.Rollback()
				return err
			}
			keys = append(keys, key)
		}
		if err = items.Err(); err != nil {
			items.Close()
			tx.Rollback()
			return err
		}
		items.Close()
		for _, key := range keys {
			revision++
			if _, err = tx.ExecContext(ctx, `UPDATE user_items SET sync_version=? WHERE user_id=? AND item_key=?`, revision, userID, key); err != nil {
				tx.Rollback()
				return err
			}
		}
		if len(keys) > 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE users SET sync_version=? WHERE id=?`, revision, userID); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
