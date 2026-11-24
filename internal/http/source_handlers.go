package httpserver

import (
	"errors"
	"net/http"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/lifecycle"
	"github.com/omni-store/omnistore/internal/sources"
	"github.com/omni-store/omnistore/internal/staticassets"
)

func (s *Server) writeSourceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sources.ErrNotFound):
		WriteError(w, r, CodeSourceNotFound, err.Error(), nil)
	default:
		WriteError(w, r, CodeValidationError, err.Error(), nil)
	}
}

// --- 管理员：存储源管理（README §25.3） ---

func (s *Server) handleAdminListSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.sources.List()
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询存储源失败", nil)
		return
	}
	WriteData(w, r, ListData{Items: list, Total: int64(len(list))})
}

func (s *Server) handleAdminPreflightSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootPath        string    `json:"root_path"`
		ExcludePatterns *[]string `json:"exclude_patterns"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	in := sources.PreflightInput{RootPath: req.RootPath}
	if req.ExcludePatterns != nil {
		in.ExcludePatterns = *req.ExcludePatterns
		in.HasPatterns = true
	}
	preview, err := s.sources.Preflight(in)
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	WriteData(w, r, preview)
}

func (s *Server) handleAdminCreateSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string    `json:"name"`
		Description     string    `json:"description"`
		RootPath        string    `json:"root_path"`
		ExcludePatterns *[]string `json:"exclude_patterns"`
		ImportExisting  bool      `json:"import_existing"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	in := sources.CreateInput{
		Name:           req.Name,
		Description:    req.Description,
		RootPath:       req.RootPath,
		ImportExisting: req.ImportExisting,
	}
	if req.ExcludePatterns != nil {
		in.ExcludePatterns = *req.ExcludePatterns
		in.HasPatterns = true
	}

	src, reconcile, err := s.files.CreateSource(in)
	if err != nil {
		if errors.Is(err, files.ErrSourceInitialization) {
			s.adminAudit(r, "create_source", audit.StatusFailed, CodeInternalError)
			WriteError(w, r, CodeInternalError, "扫描已有文件失败，未创建存储源", nil)
			return
		}
		s.adminAudit(r, "create_source", audit.StatusFailed, CodeValidationError)
		s.writeSourceError(w, r, err)
		return
	}
	s.adminAudit(r, "create_source", audit.StatusSuccess, "")
	WriteData(w, r, map[string]any{"source": src, "reconcile": reconcile})
}

func (s *Server) handleAdminGetSource(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("key"))
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	patterns, err := s.sources.ExcludePatterns(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询排除规则失败", nil)
		return
	}
	quota, err := s.files.StorageQuota(src)
	if err != nil {
		WriteError(w, r, CodeInternalError, "统计存储源用量失败", nil)
		return
	}
	ledgerUsage, err := s.files.LedgerSourceUsage(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询文件台账用量失败", nil)
		return
	}
	WriteData(w, r, map[string]any{
		"source": src, "exclude_patterns": patterns, "quota": quota, "ledger_usage_bytes": ledgerUsage,
	})
}

func (s *Server) handleAdminReconcileSource(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("key"))
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	release := s.files.LockQuotaUpdate(src.Key)
	defer release()
	result, err := s.files.ReconcileSource(src)
	if err != nil {
		s.adminAudit(r, "reconcile_source", audit.StatusFailed, CodeInternalError)
		WriteError(w, r, CodeInternalError, "校准文件台账失败", nil)
		return
	}
	s.adminAudit(r, "reconcile_source", audit.StatusSuccess, "")
	WriteData(w, r, result)
}

func (s *Server) handleAdminUpdateSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            *string   `json:"name"`
		Description     *string   `json:"description"`
		WebdavEnabled   *bool     `json:"webdav_enabled"`
		S3Enabled       *bool     `json:"s3_enabled"`
		StaticEnabled   *bool     `json:"static_enabled"`
		QuotaBytes      *int64    `json:"quota_bytes"`
		ExcludePatterns *[]string `json:"exclude_patterns"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	sourceKey := r.PathValue("key")
	if req.QuotaBytes != nil {
		releaseQuota := s.files.LockQuotaUpdate(sourceKey)
		defer releaseQuota()
	}

	src, err := s.sources.Update(sourceKey, sources.UpdateInput{
		Name:            req.Name,
		Description:     req.Description,
		WebdavEnabled:   req.WebdavEnabled,
		S3Enabled:       req.S3Enabled,
		QuotaBytes:      req.QuotaBytes,
		ExcludePatterns: req.ExcludePatterns,
	})
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	if req.StaticEnabled != nil && s.staticassets != nil {
		if err := s.setSourceStaticEnabled(r, sourceKey, *req.StaticEnabled); err != nil {
			writeStaticConfigError(w, r, err)
			return
		}
		refreshed, getErr := s.sources.Get(sourceKey)
		if getErr != nil {
			s.writeSourceError(w, r, getErr)
			return
		}
		src = refreshed
	}
	s.adminAudit(r, "update_source", audit.StatusSuccess, "")
	WriteData(w, r, src)
}

func (s *Server) setSourceStaticEnabled(r *http.Request, sourceKey string, enabled bool) error {
	staticCfg, err := s.staticassets.Config(sourceKey)
	if err != nil {
		return err
	}
	if enabled {
		if !staticCfg.Configured() {
			global, gErr := s.staticassets.GlobalSettings()
			if gErr != nil {
				return gErr
			}
			var actorID *int64
			if user := CurrentUser(r.Context()); user != nil {
				actorID = &user.ID
			}
			_, err = s.staticassets.Configure(staticassets.ConfigureInput{
				SourceKey:          sourceKey,
				PublishRoot:        "",
				CacheMode:          global.CacheMode,
				CorsMode:           global.CorsMode,
				PublicOrigin:       global.PublicOrigin,
				AllowedOrigins:     global.AllowedOrigins,
				ConfirmPublishRoot: true,
			}, actorID, s.audit)
			return err
		}
		if !staticCfg.Enabled {
			_, err = s.staticassets.Update(staticassets.UpdateInput{
				SourceKey: sourceKey,
				Enabled:   &enabled,
			})
			return err
		}
		return nil
	}
	if staticCfg.Configured() && staticCfg.Enabled {
		_, err = s.staticassets.Update(staticassets.UpdateInput{
			SourceKey: sourceKey,
			Enabled:   &enabled,
		})
		return err
	}
	return nil
}

func (s *Server) handleAdminSetSourceDisabled(disabled bool) http.HandlerFunc {
	action := "enable_source"
	if disabled {
		action = "disable_source"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.sources.SetDisabled(r.PathValue("key"), disabled); err != nil {
			s.writeSourceError(w, r, err)
			return
		}
		s.adminAudit(r, action, audit.StatusSuccess, "")
		WriteData(w, r, map[string]any{"ok": true})
	}
}

// handleAdminRebindRoot 重新绑定存储源根路径（备份恢复/磁盘迁移后）。
func (s *Server) handleAdminRebindRoot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootPath string `json:"root_path"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	src, err := s.sources.UpdateRootPath(r.PathValue("key"), req.RootPath)
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	s.adminAudit(r, "rebind_source_root", audit.StatusSuccess, req.RootPath)
	WriteData(w, r, src)
}

func (s *Server) handleAdminDeleteSource(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("key"))
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	// Site Capability 绑定守卫：仍被绑定的源必须先解除绑定。
	bound, err := s.capabilities.BoundCapabilities(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查 Site Capability 绑定失败", nil)
		return
	}
	if len(bound) > 0 {
		WriteError(w, r, CodeConflict, "存储源仍被 Site Capability 绑定，请先解除绑定", map[string]any{"bound_capabilities": bound})
		return
	}
	// 公开身份在启用期间禁止删除源，防止悄悄破坏静态链接；已关闭的配置随源一并清理。
	staticConfig, err := s.staticassets.Config(src.Key)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查静态 HTTP 配置失败", nil)
		return
	}
	if staticConfig.Enabled {
		WriteError(w, r, CodeConflict, "存储源已启用静态 HTTP，请先关闭后再删除", nil)
		return
	}
	releaseLifecycle := s.lifecycleManager().Write(lifecycle.Source(src.ID))
	defer releaseLifecycle()
	trashCount, err := s.files.SourceTrashCount(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查存储源回收站失败", nil)
		return
	}
	if trashCount > 0 {
		WriteError(w, r, CodeConflict, "存储源回收站不为空，请先恢复或永久清理其中内容", map[string]any{"trash_count": trashCount})
		return
	}
	transferCount, err := s.files.SourceTransferOperationCount(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查存储源中断移动失败", nil)
		return
	}
	if transferCount > 0 {
		WriteError(w, r, CodeConflict, "存储源存在尚未恢复的跨来源移动，请重启服务完成恢复", map[string]any{"transfer_count": transferCount})
		return
	}
	copyCount, err := s.files.SourceCopyOperationCount(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查存储源中断复制失败", nil)
		return
	}
	if copyCount > 0 {
		WriteError(w, r, CodeConflict, "存储源存在尚未恢复的跨来源复制，请重启服务完成恢复", map[string]any{"copy_count": copyCount})
		return
	}
	pathOperationCount, err := s.files.SourcePathOperationCount(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查存储源中断路径操作失败", nil)
		return
	}
	if pathOperationCount > 0 {
		WriteError(w, r, CodeConflict, "存储源存在尚未恢复的移动或永久删除，请重启服务完成恢复", map[string]any{"path_operation_count": pathOperationCount})
		return
	}
	fileUploadCount, err := s.files.SourceFileUploadOperationCount(src.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查存储源中断普通上传失败", nil)
		return
	}
	if fileUploadCount > 0 {
		WriteError(w, r, CodeConflict, "存储源存在尚未恢复的普通文件上传，请重启服务完成恢复", map[string]any{"upload_count": fileUploadCount})
		return
	}
	if s.s3Multipart != nil {
		partOperationCount, err := s.s3Multipart.SourcePartOperationCount(src.ID)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查存储源中断 Multipart 分片上传失败", nil)
			return
		}
		if partOperationCount > 0 {
			WriteError(w, r, CodeConflict, "存储源存在尚未恢复的 S3 Multipart 分片上传，请重启服务完成恢复", map[string]any{"multipart_part_count": partOperationCount})
			return
		}
		completionCount, err := s.s3Multipart.SourceCompletionOperationCount(src.ID)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查存储源中断 Multipart 完成操作失败", nil)
			return
		}
		if completionCount > 0 {
			WriteError(w, r, CodeConflict, "存储源存在尚未恢复的 S3 Multipart 完成操作，请重启服务完成恢复", map[string]any{"multipart_completion_count": completionCount})
			return
		}
	}
	if s.imagebed != nil {
		uploadCount, err := s.imagebed.SourceUploadOperationCount(src.ID)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查存储源中断图床上传失败", nil)
			return
		}
		if uploadCount > 0 {
			WriteError(w, r, CodeConflict, "存储源存在尚未恢复的图床上传，请重启服务完成恢复", map[string]any{"upload_count": uploadCount})
			return
		}
	}
	if staticConfig.Configured() && !staticConfig.Enabled {
		if err := s.staticassets.Remove(src.Key, staticConfig.Revision); err != nil {
			writeStaticConfigError(w, r, err)
			return
		}
	}
	if err := s.sources.Delete(src.Key); err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	s.adminAudit(r, "delete_source", audit.StatusSuccess, "")
	// 前端必须提示：此操作只移除 OmniStore 记录，不删除磁盘真实文件（README §10.4）。
	WriteData(w, r, map[string]any{"ok": true})
}

func (s *Server) handleAdminSetExcludePatterns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Patterns []string `json:"patterns"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	src, err := s.sources.Get(r.PathValue("key"))
	if err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	if err := s.sources.SetExcludePatterns(src.ID, req.Patterns); err != nil {
		s.writeSourceError(w, r, err)
		return
	}
	s.adminAudit(r, "update_exclude_patterns", audit.StatusSuccess, "")
	WriteData(w, r, map[string]any{"ok": true})
}

// --- 登录用户：可访问存储源列表 ---

func (s *Server) handleListMySources(w http.ResponseWriter, r *http.Request) {
	list, err := s.sources.ListForUser(CurrentUser(r.Context()))
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询存储源失败", nil)
		return
	}
	WriteData(w, r, ListData{Items: list, Total: int64(len(list))})
}

func (s *Server) handleSourceQuota(w http.ResponseWriter, r *http.Request) {
	src := s.resolveSource(w, r)
	if src == nil {
		return
	}
	if !s.authorizeSourcePath(w, r, src, "", false, false) {
		return
	}
	quota, err := s.files.StorageQuota(src)
	if err != nil {
		WriteError(w, r, CodeInternalError, "统计存储源用量失败", nil)
		return
	}
	WriteData(w, r, quota)
}
