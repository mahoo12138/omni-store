package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/lifecycle"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) adminAudit(r *http.Request, action string, status, errorCode string) {
	u := CurrentUser(r.Context())
	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &u.ID,
		EntryType: audit.EntryAdmin, Action: action,
		IPAddress: s.proxy.ClientIP(r), UserAgent: r.UserAgent(),
		Status: status, ErrorCode: errorCode,
	})
}

// --- 管理员：用户管理（README §7.2） ---

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.users.List()
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询用户失败", nil)
		return
	}
	type adminUserView struct {
		*models.User
		UsageBytes int64 `json:"usage_bytes"`
	}
	views := make([]adminUserView, 0, len(list))
	for _, user := range list {
		usage, err := s.files.UserUsage(user.ID)
		if err != nil {
			WriteError(w, r, CodeInternalError, "查询用户用量失败", nil)
			return
		}
		views = append(views, adminUserView{User: user, UsageBytes: usage})
	}
	WriteData(w, r, ListData{Items: views, Total: int64(len(views))})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		QuotaBytes  int64  `json:"quota_bytes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = models.RoleUser
	}
	if req.QuotaBytes < 0 {
		WriteError(w, r, CodeValidationError, "用户配额不能为负数", nil)
		return
	}

	u, err := s.users.Create(req.Username, req.DisplayName, req.Password, req.Role)
	if err != nil {
		s.writeUserError(w, r, err)
		return
	}
	if req.QuotaBytes != 0 {
		if err := s.users.SetQuota(u.ID, req.QuotaBytes); err != nil {
			s.writeUserError(w, r, err)
			return
		}
		u, err = s.users.GetByID(u.ID)
		if err != nil {
			WriteError(w, r, CodeInternalError, "查询用户失败", nil)
			return
		}
	}
	s.adminAudit(r, "create_user", audit.StatusSuccess, "")
	WriteData(w, r, u)
}

func (s *Server) handleAdminSetUserQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		WriteError(w, r, CodeValidationError, "非法用户 ID", nil)
		return
	}
	var req struct {
		QuotaBytes int64 `json:"quota_bytes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	release := s.files.LockUserQuotaUpdate(id)
	defer release()
	if err := s.users.SetQuota(id, req.QuotaBytes); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	user, err := s.users.GetByID(id)
	if err != nil {
		s.writeUserError(w, r, err)
		return
	}
	quota, err := s.files.UserQuota(user)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询用户配额失败", nil)
		return
	}
	s.adminAudit(r, "update_user_quota", audit.StatusSuccess, "")
	WriteData(w, r, quota)
}

// handleAdminSetUserDisabled 处理禁用与启用。
func (s *Server) handleAdminSetUserDisabled(disabled bool) http.HandlerFunc {
	action := "enable_user"
	if disabled {
		action = "disable_user"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			WriteError(w, r, CodeValidationError, "非法用户 ID", nil)
			return
		}
		if id == CurrentUser(r.Context()).ID {
			WriteError(w, r, CodeValidationError, "不能操作自己的账号", nil)
			return
		}
		if err := s.users.SetDisabled(id, disabled); err != nil {
			s.writeUserError(w, r, err)
			return
		}
		if disabled {
			// 禁用后所有入口立即失效（README §26.2）。
			_ = s.sessions.DeleteByUser(id)
		}
		s.adminAudit(r, action, audit.StatusSuccess, "")
		WriteData(w, r, map[string]any{"ok": true})
	}
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		WriteError(w, r, CodeValidationError, "非法用户 ID", nil)
		return
	}
	if id == CurrentUser(r.Context()).ID {
		WriteError(w, r, CodeValidationError, "不能删除自己的账号", nil)
		return
	}
	releaseLifecycle := s.lifecycleManager().Write(lifecycle.User(id))
	defer releaseLifecycle()
	trashCount, err := s.files.UserTrashCount(id)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查用户回收站失败", nil)
		return
	}
	if trashCount > 0 {
		WriteError(w, r, CodeConflict, "该用户的回收站不为空，请先恢复或永久清理其中内容", map[string]any{"trash_count": trashCount})
		return
	}
	fileUploadCount, err := s.files.UserFileUploadOperationCount(id)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查用户中断普通上传失败", nil)
		return
	}
	if fileUploadCount > 0 {
		WriteError(w, r, CodeConflict, "该用户存在尚未恢复的普通文件上传，请重启服务完成恢复", map[string]any{"upload_count": fileUploadCount})
		return
	}
	pathOperationCount, err := s.files.UserPathOperationCount(id)
	if err != nil {
		WriteError(w, r, CodeInternalError, "检查用户中断路径操作失败", nil)
		return
	}
	if pathOperationCount > 0 {
		WriteError(w, r, CodeConflict, "该用户存在尚未恢复的移动或永久删除，请重启服务完成恢复", map[string]any{"path_operation_count": pathOperationCount})
		return
	}
	if s.imagebed != nil {
		imageUploadCount, err := s.imagebed.UserUploadOperationCount(id)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查用户中断图床上传失败", nil)
			return
		}
		if imageUploadCount > 0 {
			WriteError(w, r, CodeConflict, "该用户存在尚未恢复的图床上传，请重启服务完成恢复", map[string]any{"image_upload_count": imageUploadCount})
			return
		}
	}
	if s.s3Multipart != nil {
		partOperationCount, err := s.s3Multipart.UserPartOperationCount(id)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查用户中断 Multipart 分片上传失败", nil)
			return
		}
		if partOperationCount > 0 {
			WriteError(w, r, CodeConflict, "该用户存在尚未恢复的 S3 Multipart 分片上传，请重启服务完成恢复", map[string]any{"multipart_part_count": partOperationCount})
			return
		}
		completionCount, err := s.s3Multipart.UserCompletionOperationCount(id)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查用户中断 Multipart 完成操作失败", nil)
			return
		}
		if completionCount > 0 {
			WriteError(w, r, CodeConflict, "该用户存在尚未恢复的 S3 Multipart 完成操作，请重启服务完成恢复", map[string]any{"multipart_completion_count": completionCount})
			return
		}
	}
	if err := s.users.Delete(id); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	s.adminAudit(r, "delete_user", audit.StatusSuccess, "")
	WriteData(w, r, map[string]any{"ok": true})
}

func (s *Server) handleAdminRevokeUserCredentials(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		WriteError(w, r, CodeValidationError, "非法用户 ID", nil)
		return
	}
	if id == CurrentUser(r.Context()).ID {
		WriteError(w, r, CodeValidationError, "不能通过管理入口撤销自己的凭据", nil)
		return
	}
	result, err := s.users.RevokeCredentials(id)
	if err != nil {
		s.writeUserError(w, r, err)
		return
	}
	s.adminAudit(r, "revoke_user_credentials", audit.StatusSuccess, "")
	WriteData(w, r, result)
}

// --- 用户自助（README §7.3） ---

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := CurrentUser(r.Context())
	if err := s.users.UpdateDisplayName(u.ID, req.DisplayName); err != nil {
		WriteError(w, r, CodeValidationError, err.Error(), nil)
		return
	}
	updated, err := s.users.GetByID(u.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询失败", nil)
		return
	}
	WriteData(w, r, updated)
}

func (s *Server) handleMyQuota(w http.ResponseWriter, r *http.Request) {
	quota, err := s.files.UserQuota(CurrentUser(r.Context()))
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询用户配额失败", nil)
		return
	}
	WriteData(w, r, quota)
}

// ActivityItem 是首页"最近活动"面板的一项（docs/home.png）。
type ActivityItem struct {
	ID           int64  `json:"id"`
	Action       string `json:"action"`
	Title        string `json:"title"`
	SourceName   string `json:"source_name,omitempty"`
	RelativePath string `json:"relative_path,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// handleMyActivity 返回当前用户最近的活动事件（来自 audit_logs）。
// 只挑选对用户有意义的"高阶操作"：上传/删除文件/目录、图床上传/删除等，
// 过滤掉 login/password 这种与个人仪表盘无关的条目。
func (s *Server) handleMyActivity(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	limit := 8
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	entries, err := s.audit.UserActivity(u.ID, limit)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询活动失败", nil)
		return
	}
	out := make([]ActivityItem, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ActivityItem{
			ID:           entry.ID,
			Action:       entry.Action,
			Title:        humanizeActivity(entry.Action, entry.RelativePath, entry.SourceName),
			SourceName:   entry.SourceName,
			RelativePath: entry.RelativePath,
			CreatedAt:    entry.CreatedAt,
		})
	}
	WriteData(w, r, ListData{Items: out, Total: int64(len(out))})
}

type RecentFileItem struct {
	ID         int64     `json:"id"`
	SourceKey  string    `json:"source_key"`
	SourceName string    `json:"source_name"`
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	AccessedAt string    `json:"accessed_at"`
}

// handleMyRecentFiles returns recently uploaded/downloaded or reorganized files
// that still exist and are currently readable by the requesting user.
func (s *Server) handleMyRecentFiles(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r.Context())
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	candidates, err := s.files.ListRecentFiles(user.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询最近文件失败", nil)
		return
	}

	items := make([]RecentFileItem, 0, limit)
	for _, candidate := range candidates {
		source, err := s.sources.GetByID(candidate.SourceID)
		if err != nil || source.IsDisabled {
			continue
		}
		allowed, err := s.sources.CanReadPath(user, candidate.SourceKey, candidate.RelativePath)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查最近文件权限失败", nil)
			return
		}
		if !allowed {
			continue
		}
		entry, err := s.files.Stat(source, candidate.RelativePath)
		if err != nil || entry.Type != files.TypeFile {
			continue
		}
		items = append(items, RecentFileItem{
			ID: candidate.ID, SourceKey: candidate.SourceKey, SourceName: candidate.SourceName,
			Path: candidate.RelativePath, Name: entry.Name, Size: entry.Size,
			ModifiedAt: entry.MTime, AccessedAt: candidate.AccessedAt,
		})
		if len(items) == limit {
			break
		}
	}
	WriteData(w, r, ListData{Items: items, Total: int64(len(items))})
}

type FavoriteItem struct {
	ID         int64     `json:"id"`
	SourceKey  string    `json:"source_key"`
	SourceName string    `json:"source_name"`
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	CreatedAt  string    `json:"created_at"`
}

// handleMyFavorites shows only targets that still exist and are currently readable.
func (s *Server) handleMyFavorites(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r.Context())
	candidates, err := s.files.ListFavorites(user.ID)
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询收藏失败", nil)
		return
	}

	items := make([]FavoriteItem, 0, len(candidates))
	for _, candidate := range candidates {
		source, err := s.sources.GetByID(candidate.SourceID)
		if err != nil || source.IsDisabled {
			continue
		}
		allowed, err := s.sources.CanReadPath(user, candidate.SourceKey, candidate.RelativePath)
		if err != nil {
			WriteError(w, r, CodeInternalError, "检查收藏权限失败", nil)
			return
		}
		if !allowed {
			continue
		}
		entry, err := s.files.Stat(source, candidate.RelativePath)
		if errors.Is(err, files.ErrNotFound) {
			// Remove dead path references so a later file created at the same path
			// does not unexpectedly inherit an old favorite.
			_, _ = s.files.DeleteFavorite(user.ID, candidate.ID)
			continue
		}
		if err != nil || (entry.Type != files.TypeFile && entry.Type != files.TypeDir) {
			continue
		}
		items = append(items, FavoriteItem{
			ID: candidate.ID, SourceKey: candidate.SourceKey, SourceName: candidate.SourceName,
			Path: candidate.RelativePath, Name: entry.Name, Type: string(entry.Type),
			Size: entry.Size, ModifiedAt: entry.MTime, CreatedAt: candidate.CreatedAt,
		})
	}
	WriteData(w, r, ListData{Items: items, Total: int64(len(items))})
}

func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceKey string `json:"source_key"`
		Path      string `json:"path"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	source := s.resolveSourceKey(w, r, req.SourceKey)
	if source == nil {
		return
	}
	relativePath, err := security.NormalizeRelPath(req.Path)
	if err != nil || relativePath == "" {
		WriteError(w, r, CodePathInvalid, "收藏路径无效", nil)
		return
	}
	if !s.authorizeSourcePath(w, r, source, relativePath, false, false) {
		return
	}
	entry, err := s.files.Stat(source, relativePath)
	if err != nil {
		writeFileError(w, r, err)
		return
	}
	if entry.Type != files.TypeFile && entry.Type != files.TypeDir {
		WriteError(w, r, CodePathInvalid, "仅支持收藏文件或文件夹", nil)
		return
	}
	user := CurrentUser(r.Context())
	id, err := s.files.AddFavorite(user.ID, source.ID, relativePath)
	if err != nil {
		if errors.Is(err, files.ErrFavoriteLimit) {
			WriteError(w, r, CodeValidationError, err.Error(), nil)
			return
		}
		WriteError(w, r, CodeInternalError, "添加收藏失败", nil)
		return
	}
	WriteData(w, r, FavoriteItem{
		ID: id, SourceKey: source.Key, SourceName: source.Name, Path: relativePath,
		Name: entry.Name, Type: string(entry.Type), Size: entry.Size,
		ModifiedAt: entry.MTime,
	})
}

func (s *Server) handleDeleteFavorite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("favoriteID"), 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, r, CodeValidationError, "收藏标识无效", nil)
		return
	}
	deleted, err := s.files.DeleteFavorite(CurrentUser(r.Context()).ID, id)
	if err != nil {
		WriteError(w, r, CodeInternalError, "移除收藏失败", nil)
		return
	}
	if !deleted {
		WriteError(w, r, CodeFileNotFound, "收藏不存在", nil)
		return
	}
	WriteData(w, r, map[string]any{"ok": true})
}

// humanizeActivity 把 audit action 翻译成中文短句。
func humanizeActivity(action, relPath, sourceName string) string {
	filename := ""
	if relPath != "" {
		if i := strings.LastIndex(relPath, "/"); i >= 0 {
			filename = relPath[i+1:]
		} else {
			filename = relPath
		}
	}
	loc := sourceName
	switch action {
	case "image_upload":
		if filename != "" {
			if loc != "" {
				return "上传了 " + filename + " 到 " + loc
			}
			return "上传了 " + filename
		}
		return "上传了图片"
	case "image_delete":
		if filename != "" {
			return "删除了图片 " + filename
		}
		return "删除了图片"
	case "upload":
		if filename != "" {
			if loc != "" {
				return "上传了 " + filename + " 到 " + loc
			}
			return "上传了 " + filename
		}
		return "上传了文件"
	case "delete":
		if filename != "" {
			return "删除了 " + filename
		}
		return "删除了文件"
	case "trash":
		if filename != "" {
			return "将 " + filename + " 移入了回收站"
		}
		return "将文件移入了回收站"
	case "restore":
		if filename != "" {
			return "恢复了 " + filename
		}
		return "恢复了文件"
	case "purge":
		if filename != "" {
			return "永久删除了 " + filename
		}
		return "永久清理了回收站文件"
	case "create_folder":
		if filename != "" {
			return "创建了文件夹 " + filename
		}
		return "创建了文件夹"
	case "rename":
		if filename != "" {
			return "重命名了 " + filename
		}
		return "重命名了文件"
	case "move":
		if filename != "" {
			return "移动了 " + filename
		}
		return "移动了文件"
	case "copy":
		if filename != "" {
			return "复制了 " + filename
		}
		return "复制了文件"
	case "create_share":
		if filename != "" {
			return "分享了 " + filename
		}
		return "创建了文件分享"
	case "revoke_share":
		if filename != "" {
			return "撤销了 " + filename + " 的分享"
		}
		return "撤销了文件分享"
	default:
		// 未知动作：保留原文。
		return action
	}
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := CurrentUser(r.Context())

	// SSO 建号账号没有本地密码可校验，提示去 IdP 修改而不是误导性报"旧密码错误"。
	if has, err := s.users.HasPassword(u.ID); err == nil && !has {
		WriteError(w, r, CodeForbidden, "当前账号通过 SSO 登录且未设置本地密码，请在身份提供方修改密码", nil)
		return
	}

	hash, err := s.users.PasswordHashByID(u.ID)
	if err != nil || !auth.VerifyPassword(hash, req.OldPassword) {
		WriteError(w, r, CodeForbidden, "旧密码错误", nil)
		return
	}
	if err := s.users.UpdatePasswordKeepingSession(u.ID, req.NewPassword, currentSession(r.Context()).SessionID); err != nil {
		s.writeUserError(w, r, err)
		return
	}

	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &u.ID,
		EntryType: audit.EntryWeb, Action: "change_password",
		IPAddress: s.proxy.ClientIP(r), UserAgent: r.UserAgent(),
		Status: audit.StatusSuccess,
	})
	WriteData(w, r, map[string]any{"ok": true})
}
