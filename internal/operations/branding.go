package operations

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	settingKeyInstanceName = "instance_name"
	DefaultInstanceName    = "OmniStore"
	MaxInstanceNameRunes   = 64
)

// ErrInstanceNameTooLong 表示实例名称超过字符数上限。
var ErrInstanceNameTooLong = errors.New("实例名称不能超过 64 个字符")

// InstanceName 返回当前实例显示名称，未配置时回退默认值。
func (s *Service) InstanceName() string {
	var val string
	if err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`,
		settingKeyInstanceName).Scan(&val); err != nil {
		return DefaultInstanceName
	}
	if name := strings.TrimSpace(val); name != "" {
		return name
	}
	return DefaultInstanceName
}

// SetInstanceName 保存或重置实例显示名称（空字符串表示恢复默认名称）。
func (s *Service) SetInstanceName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if utf8.RuneCountInString(name) > MaxInstanceNameRunes {
		return "", ErrInstanceNameTooLong
	}
	now := time.Now().UTC()
	var err error
	if name == "" {
		_, err = s.db.Exec(`DELETE FROM system_settings WHERE key = ?`, settingKeyInstanceName)
	} else {
		_, err = s.db.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)
  ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			settingKeyInstanceName, name, now)
	}
	if err != nil {
		return "", err
	}
	return s.InstanceName(), nil
}
