package db

import (
	"errors"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueConstraint 判断错误是否为 SQLite 唯一约束（含主键）冲突。
// column 非空时同时匹配冲突列名（如 "image_bed_tokens.token_id"），避免多唯一键表误判。
func IsUniqueConstraint(err error, column string) bool {
	if err == nil {
		return false
	}
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	code := sqlErr.Code()
	if code != sqlite3.SQLITE_CONSTRAINT_UNIQUE && code != sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
		return false
	}
	if column == "" {
		return true
	}
	return strings.Contains(sqlErr.Error(), column)
}
