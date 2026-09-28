// Package store 负责 SQLite 连接与建表迁移。
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，注册名为 "sqlite"
)

// migrations 按顺序执行的建表语句。
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS memos(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id TEXT NOT NULL,
  user_email TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  date_type TEXT NOT NULL CHECK(date_type IN ('solar','lunar')),
  repeat TEXT NOT NULL CHECK(repeat IN ('yearly','monthly','daily','once')),
  month INTEGER, day INTEGER,
  lunar_month INTEGER, lunar_day INTEGER, lunar_leap INTEGER NOT NULL DEFAULT 0,
  advance_offsets TEXT NOT NULL DEFAULT '[]',
  once_date TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_memos_user ON memos(user_id)`,
	`CREATE TABLE IF NOT EXISTS send_log(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  memo_id INTEGER NOT NULL,
  user_id TEXT NOT NULL,
  due_date TEXT NOT NULL,
  offset_days INTEGER NOT NULL,
  sent_at TEXT,
  UNIQUE(memo_id, due_date, offset_days)
)`,
}

// Open 打开 SQLite 数据库，设置 busy_timeout 并启用 WAL，自动执行建表迁移。
func Open(path string) (*sql.DB, error) {
	// busy_timeout 避免并发写时立即报 SQLITE_BUSY
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s 失败: %w", path, err)
	}
	// SQLite 单连接写更安全，避免 database is locked
	db.SetMaxOpenConns(1)
	for i, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			db.Close()
			return nil, fmt.Errorf("执行迁移 #%d 失败: %w", i+1, err)
		}
	}
	return db, nil
}
