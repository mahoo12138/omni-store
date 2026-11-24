// Package audit 实现轻量审计日志（README §20）。
package audit

import (
	"database/sql"
	"log/slog"
	"strings"
	"time"
)

// actor_type 取值。
const (
	ActorUser      = "user"
	ActorAnonymous = "anonymous"
	ActorSystem    = "system"
)

// entry_type 取值。
const (
	EntryWeb               = "web"
	EntryWebDAV            = "webdav"
	EntryImageBed          = "image_bed"
	EntryAnonymousImageBed = "anonymous_image_bed"
	EntryAdmin             = "admin"
	EntryCLI               = "cli"
	EntryS3                = "s3"
)

// status 取值。
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// Entry 是一条审计记录。
type Entry struct {
	ActorType          string
	ActorUserID        *int64
	EntryType          string
	Action             string
	StorageSourceID    *int64
	RelativePath       string
	TargetRelativePath string
	IPAddress          string
	UserAgent          string
	Status             string
	ErrorCode          string
}

// Logger 写审计日志并维护保留上限。
type Logger struct {
	db         *sql.DB
	rdb        *sql.DB
	enabled    bool
	maxEntries int
	slog       *slog.Logger
}

// New 创建审计日志器。maxEntries = 0 表示不限制。
func New(db *sql.DB, enabled bool, maxEntries int, logger *slog.Logger) *Logger {
	return &Logger{db: db, enabled: enabled, maxEntries: maxEntries, slog: logger}
}

// SetReadDB 注入只读连接池供审计查询与用户活动查询使用。
func (l *Logger) SetReadDB(rdb *sql.DB) {
	l.rdb = rdb
}

func (l *Logger) readDB() *sql.DB {
	if l.rdb != nil {
		return l.rdb
	}
	return l.db
}

// UserActivityEntry 表示个人仪表盘展示的一条高阶用户活动。
type UserActivityEntry struct {
	ID           int64
	Action       string
	RelativePath string
	SourceName   string
	CreatedAt    string
}

// UserActivity 返回指定用户最近的高阶操作事件（排除 login/password 类杂项）。
func (l *Logger) UserActivity(userID int64, limit int) ([]UserActivityEntry, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	rows, err := l.readDB().Query(`SELECT a.id, a.action, a.relative_path, a.created_at,
       COALESCE(s.name, '') AS source_name
  FROM audit_logs a
  LEFT JOIN storage_sources s ON s.id = a.storage_source_id
  WHERE a.actor_type = 'user' AND a.actor_user_id = ?
    AND a.action NOT IN ('login_success', 'login_failed', 'change_password')
  ORDER BY a.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UserActivityEntry, 0, limit)
	for rows.Next() {
		var item UserActivityEntry
		var rel *string
		if err := rows.Scan(&item.ID, &item.Action, &rel, &item.CreatedAt, &item.SourceName); err != nil {
			return nil, err
		}
		if rel != nil {
			item.RelativePath = *rel
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Log 写入一条审计记录。审计失败只记录应用日志，不影响业务。
func (l *Logger) Log(e Entry) {
	if !l.enabled {
		return
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err := l.db.Exec(`INSERT INTO audit_logs
  (actor_type, actor_user_id, entry_type, action, storage_source_id, relative_path, target_relative_path,
   ip_address, user_agent, status, error_code, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ActorType, e.ActorUserID, e.EntryType, e.Action,
		e.StorageSourceID, nullable(e.RelativePath), nullable(e.TargetRelativePath),
		nullable(e.IPAddress), nullable(e.UserAgent), e.Status, nullable(e.ErrorCode),
		time.Now().UTC())
	if err != nil {
		l.slog.Error("写审计日志失败", "err", err, "action", e.Action)
		return
	}
	l.trim()
}

// trim 超过 max_entries 时删除最旧记录。
func (l *Logger) trim() {
	if l.maxEntries <= 0 {
		return
	}
	_, err := l.db.Exec(`DELETE FROM audit_logs WHERE id NOT IN (
    SELECT id FROM audit_logs ORDER BY id DESC LIMIT ?)`, l.maxEntries)
	if err != nil {
		l.slog.Error("清理审计日志失败", "err", err)
	}
}

// LogEntry 是审计查询返回结构。
type LogEntry struct {
	ID                 int64     `json:"id"`
	ActorType          string    `json:"actor_type"`
	ActorUserID        *int64    `json:"actor_user_id"`
	EntryType          string    `json:"entry_type"`
	Action             string    `json:"action"`
	StorageSourceID    *int64    `json:"storage_source_id"`
	StorageSourceName  *string   `json:"storage_source_name"`
	RelativePath       *string   `json:"relative_path"`
	TargetRelativePath *string   `json:"target_relative_path"`
	IPAddress          *string   `json:"ip_address"`
	UserAgent          *string   `json:"user_agent"`
	Status             string    `json:"status"`
	ErrorCode          *string   `json:"error_code"`
	CreatedAt          time.Time `json:"created_at"`
}

// QueryOptions 是管理后台审计日志的筛选与分页参数。
type QueryOptions struct {
	Page       int
	PageSize   int
	ActorType  string
	EntryType  string
	Status     string
	SearchText string
}

// Query 按筛选条件返回审计日志。所有筛选值都通过参数绑定传给 SQLite。
func (l *Logger) Query(opts QueryOptions) ([]*LogEntry, int64, error) {
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.PageSize < 1 {
		opts.PageSize = 10
	}
	if opts.PageSize > 200 {
		opts.PageSize = 200
	}

	where := []string{"1 = 1"}
	args := []any{}
	addExact := func(column, value string) {
		if value == "" {
			return
		}
		where = append(where, column+" = ?")
		args = append(args, value)
	}
	addExact("a.actor_type", opts.ActorType)
	addExact("a.entry_type", opts.EntryType)
	addExact("a.status", opts.Status)

	if query := strings.TrimSpace(opts.SearchText); query != "" {
		query = "%" + escapeLike(query) + "%"
		where = append(where, `(a.action LIKE ? ESCAPE '\' OR s.name LIKE ? ESCAPE '\'
      OR a.relative_path LIKE ? ESCAPE '\' OR a.target_relative_path LIKE ? ESCAPE '\'
      OR a.ip_address LIKE ? ESCAPE '\' OR a.error_code LIKE ? ESCAPE '\')`)
		for range 6 {
			args = append(args, query)
		}
	}

	whereSQL := strings.Join(where, " AND ")
	var total int64
	if err := l.readDB().QueryRow(`SELECT COUNT(*) FROM audit_logs a
  LEFT JOIN storage_sources s ON s.id = a.storage_source_id WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, opts.PageSize, (opts.Page-1)*opts.PageSize)
	rows, err := l.readDB().Query(`SELECT
    a.id, a.actor_type, a.actor_user_id, a.entry_type, a.action, a.storage_source_id, s.name,
    a.relative_path, a.target_relative_path, a.ip_address, a.user_agent, a.status, a.error_code, a.created_at
  FROM audit_logs a LEFT JOIN storage_sources s ON s.id = a.storage_source_id
  WHERE `+whereSQL+` ORDER BY a.id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []*LogEntry{}
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorUserID, &e.EntryType, &e.Action,
			&e.StorageSourceID, &e.StorageSourceName, &e.RelativePath, &e.TargetRelativePath, &e.IPAddress, &e.UserAgent,
			&e.Status, &e.ErrorCode, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &e)
	}
	return out, total, rows.Err()
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// Recent 返回最近 n 条审计日志，供管理概览等摘要场景使用。
func (l *Logger) Recent(n int) ([]*LogEntry, error) {
	entries, _, err := l.Query(QueryOptions{Page: 1, PageSize: n})
	return entries, err
}
