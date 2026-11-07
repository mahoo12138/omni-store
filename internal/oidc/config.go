// Package oidc 封装 OIDC 单点登录（Casdoor 等 IdP）的配置存储与协议客户端。
// 配置存 system_settings（key=oidc_config），管理员可在后台热配置，无需重启。
package oidc

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SettingKey 是 system_settings 中保存 OIDC 配置 JSON 的键。
const SettingKey = "oidc_config"

// DefaultScopes 是未配置 scopes 时使用的默认授权范围。
var DefaultScopes = []string{"openid", "profile", "email"}

// Config 是管理员可配置的 SSO 设置。
type Config struct {
	Enabled          bool     `json:"enabled"`
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"client_id"`
	ClientSecret     string   `json:"client_secret,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	AutoLinkUsername bool     `json:"auto_link_username"`
	AutoCreateUsers  bool     `json:"auto_create_users"`
}

// DisabledConfig 返回未启用 SSO 的默认配置。
func DisabledConfig() *Config {
	return &Config{}
}

// ScopesOrDefault 返回授权范围，保证始终包含 openid。
func (c *Config) ScopesOrDefault() []string {
	scopes := make([]string, 0, len(c.Scopes)+1)
	for _, s := range c.Scopes {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	if len(scopes) == 0 {
		return append([]string(nil), DefaultScopes...)
	}
	for _, s := range scopes {
		if s == "openid" {
			return scopes
		}
	}
	return append([]string{"openid"}, scopes...)
}

// Validate 校验配置；enabled 时 issuer 与 client_id 必填。
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if u, err := url.Parse(strings.TrimSpace(c.Issuer)); err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("Issuer 地址无效（须为 http/https URL）")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("Client ID 不能为空")
	}
	return nil
}

// Settings 读写 system_settings 中的 SSO 配置。
type Settings struct {
	db *sql.DB
}

// NewSettings 创建配置存储。
func NewSettings(db *sql.DB) *Settings {
	return &Settings{db: db}
}

// Load 读取配置；未配置或 JSON 损坏时返回未启用的默认值。
func (s *Settings) Load() (*Config, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, SettingKey).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return DisabledConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := DisabledConfig()
	if err := json.Unmarshal([]byte(val), cfg); err != nil {
		return DisabledConfig(), nil
	}
	return cfg, nil
}

// Save 保存配置（upsert，复用 branding 的 system_settings 写入模式）。
func (s *Settings) Save(cfg *Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)
	  ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		SettingKey, string(raw), time.Now().UTC())
	return err
}
