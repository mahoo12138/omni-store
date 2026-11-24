package staticassets

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
)

const settingKeyStaticHTTP = "static_http_settings"

// GlobalSettings 是全局静态 HTTP 访问参数（与具体存储源解耦，统一控制 CDN/缓存/跨域）。
type GlobalSettings struct {
	PublicOrigin   string   `json:"public_origin"`
	CacheMode      string   `json:"cache_mode"`
	CorsMode       string   `json:"cors_mode"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// SourceEndpointSummary 是已启用静态 HTTP 的存储源只读地址摘要。
type SourceEndpointSummary struct {
	SourceKey     string `json:"source_key"`
	SourceName    string `json:"source_name"`
	PublicAssetID string `json:"public_asset_id"`
	BaseURL       string `json:"base_url"`
}

// GlobalSettingsView 是管理后台读取/保存全局静态 HTTP 设置的完整视图。
type GlobalSettingsView struct {
	PublicOrigin   string                  `json:"public_origin"`
	CacheMode      string                  `json:"cache_mode"`
	CorsMode       string                  `json:"cors_mode"`
	AllowedOrigins []string                `json:"allowed_origins"`
	Sources        []SourceEndpointSummary `json:"sources"`
}

// GlobalSettings 读取全局静态 HTTP 访问参数；未保存过时返回默认策略。
func (s *Service) GlobalSettings() (*GlobalSettings, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, settingKeyStaticHTTP).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return &GlobalSettings{
			CacheMode:      CacheShort,
			CorsMode:       CorsPublic,
			AllowedOrigins: []string{},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	var out GlobalSettings
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out.CacheMode == "" {
		out.CacheMode = CacheShort
	}
	if out.CorsMode == "" {
		out.CorsMode = CorsPublic
	}
	if out.AllowedOrigins == nil {
		out.AllowedOrigins = []string{}
	}
	return &out, nil
}

// GlobalSettingsView 返回全局 HTTP 参数与当前已开启静态 HTTP 的存储源访问地址列表。
func (s *Service) GlobalSettingsView() (*GlobalSettingsView, error) {
	settings, err := s.GlobalSettings()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT src.key, src.name, a.public_asset_id, a.public_origin
FROM source_static_access a
JOIN storage_sources src ON src.id = a.storage_source_id
WHERE a.enabled = 1
ORDER BY src.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	endpoints := []SourceEndpointSummary{}
	for rows.Next() {
		var key, name, assetID, rowOrigin string
		if err := rows.Scan(&key, &name, &assetID, &rowOrigin); err != nil {
			return nil, err
		}
		origin := rowOrigin
		if origin == "" {
			origin = settings.PublicOrigin
		}
		endpoints = append(endpoints, SourceEndpointSummary{
			SourceKey:     key,
			SourceName:    name,
			PublicAssetID: assetID,
			BaseURL:       s.BaseURL(origin, assetID),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &GlobalSettingsView{
		PublicOrigin:   settings.PublicOrigin,
		CacheMode:      settings.CacheMode,
		CorsMode:       settings.CorsMode,
		AllowedOrigins: settings.AllowedOrigins,
		Sources:        endpoints,
	}, nil
}

// SaveGlobalSettings 保存全局 HTTP 访问参数，并同步到已存在的存储源静态配置。
func (s *Service) SaveGlobalSettings(in GlobalSettings, actorUserID *int64, auditLogger *audit.Logger) (*GlobalSettingsView, error) {
	origin, err := NormalizePublicOrigin(in.PublicOrigin)
	if err != nil {
		return nil, err
	}
	cacheMode := in.CacheMode
	if cacheMode == "" {
		cacheMode = CacheShort
	}
	corsMode := in.CorsMode
	if corsMode == "" {
		corsMode = CorsPublic
	}
	if err := validateModes(cacheMode, corsMode); err != nil {
		return nil, err
	}
	origins, err := normalizeOriginList(in.AllowedOrigins)
	if err != nil {
		return nil, err
	}

	normalized := GlobalSettings{
		PublicOrigin:   origin,
		CacheMode:      cacheMode,
		CorsMode:       corsMode,
		AllowedOrigins: origins,
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}

	configMu.Lock()
	defer configMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingKeyStaticHTTP, string(encoded), now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE source_static_access
SET public_origin = ?, cache_mode = ?, cors_mode = ?, revision = revision + 1, updated_at = ?`,
		origin, cacheMode, corsMode, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM source_static_origins`); err != nil {
		return nil, err
	}
	if len(origins) > 0 {
		rows, err := tx.Query(`SELECT storage_source_id FROM source_static_access`)
		if err != nil {
			return nil, err
		}
		var sourceIDs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			sourceIDs = append(sourceIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		for _, sourceID := range sourceIDs {
			for _, allowed := range origins {
				if _, err := tx.Exec(`INSERT INTO source_static_origins (storage_source_id, origin, created_at) VALUES (?, ?, ?)`,
					sourceID, allowed, now); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	logConfigAudit(auditLogger, actorUserID, "static_http_settings_update", "", origin, "")
	return s.GlobalSettingsView()
}
