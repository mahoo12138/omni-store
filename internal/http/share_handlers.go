package httpserver

import (
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/shares"
)

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	items, err := s.shares.List(CurrentUser(r.Context()))
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询分享失败", nil)
		return
	}
	WriteData(w, r, ListData{Items: items, Total: int64(len(items))})
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceKey    string     `json:"source_key"`
		Path         string     `json:"path"`
		Password     string     `json:"password"`
		ExpiresAt    *time.Time `json:"expires_at"`
		MaxDownloads int64      `json:"max_downloads"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	share, err := s.shares.Create(CurrentUser(r.Context()), shares.CreateInput{
		SourceKey: req.SourceKey, Path: req.Path, Password: req.Password,
		ExpiresAt: req.ExpiresAt, MaxDownloads: req.MaxDownloads,
	})
	if err != nil {
		s.writeShareError(w, r, err)
		return
	}
	if src, getErr := s.sources.Get(share.SourceKey); getErr == nil {
		s.fileAudit(r, "create_share", src, share.RelativePath, "", nil)
	}
	WriteData(w, r, share)
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	share, err := s.shares.Delete(CurrentUser(r.Context()), r.PathValue("shareKey"))
	if err != nil {
		s.writeShareError(w, r, err)
		return
	}
	if src, getErr := s.sources.Get(share.SourceKey); getErr == nil {
		s.fileAudit(r, "revoke_share", src, share.RelativePath, "", nil)
	}
	WriteData(w, r, map[string]any{"ok": true})
}

func (s *Server) handlePublicShareInfo(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("shareKey")
	info, err := s.shares.PublicInfo(key, shareSessionToken(r, key))
	if err != nil {
		s.writePublicShareError(w, r, err)
		return
	}
	WriteData(w, r, info)
}

func (s *Server) handlePublicShareUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	token, expires, err := s.shares.Unlock(r.PathValue("shareKey"), req.Password, s.proxy.ClientIP(r))
	if err != nil {
		if errors.Is(err, shares.ErrPassword) {
			WriteError(w, r, CodeUnauthorized, "访问密码错误或尝试次数过多", nil)
			return
		}
		s.writePublicShareError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: shares.AccessCookieName(r.PathValue("shareKey")), Value: token, Path: "/", Expires: expires,
		MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.Security.CookieSecure,
	})
	WriteData(w, r, map[string]any{"ok": true})
}

func (s *Server) handlePublicShareBrowse(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	key := r.PathValue("shareKey")
	result, err := s.shares.Browse(key, shareSessionToken(r, key), r.URL.Query().Get("path"), files.ListOptions{
		Page: page, PageSize: pageSize, Sort: r.URL.Query().Get("sort"), Order: r.URL.Query().Get("order"),
	})
	if err != nil {
		s.writePublicShareError(w, r, err)
		return
	}
	WriteData(w, r, result)
}

func (s *Server) handlePublicShareRaw(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("shareKey")
	share, src, relPath, err := s.shares.Resolve(key, shareSessionToken(r, key), r.PathValue("childPath"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, info, unlock, err := s.shares.Files().OpenForReadContext(r.Context(), src, relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer unlock()
	defer f.Close()
	if r.Method == http.MethodGet {
		if err := s.shares.ReserveDownload(share.ID); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	filename := sanitizeFilename(path.Base("/" + relPath))
	if err := setUserContentHeaders(w, f, filename, r.URL.Query().Get("download") == "1"); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	s.audit.Log(audit.Entry{ActorType: audit.ActorAnonymous, EntryType: audit.EntryWeb, Action: "share_download",
		StorageSourceID: &src.ID, RelativePath: strings.TrimPrefix(relPath, "/"), IPAddress: s.proxy.ClientIP(r),
		UserAgent: r.UserAgent(), Status: audit.StatusSuccess})
	serveContentWithDeadline(w, r, info.Name(), info.ModTime(), f)
}

// handlePublicShareArchive 以流式 ZIP 打包下载分享目录（含 ?path= 指定的子目录）。
// 打包过程直接写入响应，不在磁盘生成中间包；下载消耗一次分享下载次数。
func (s *Server) handlePublicShareArchive(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("shareKey")
	share, src, relPath, err := s.shares.Resolve(key, shareSessionToken(r, key), r.URL.Query().Get("path"))
	if err != nil {
		s.writePublicShareError(w, r, err)
		return
	}
	if share.EntryType != files.TypeDir {
		http.NotFound(w, r)
		return
	}
	plan, err := s.files.PrepareStreamArchive(src, relPath)
	if err != nil {
		if errors.Is(err, files.ErrLocked) {
			writeFileError(w, r, err)
			return
		}
		http.NotFound(w, r)
		return
	}
	if err := s.shares.ReserveDownload(share.ID); err != nil {
		plan.Release()
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": sanitizeFilename(plan.Filename())}))
	w.Header().Set("Cache-Control", "private, no-store")
	s.audit.Log(audit.Entry{ActorType: audit.ActorAnonymous, EntryType: audit.EntryWeb, Action: "share_archive_download",
		StorageSourceID: &src.ID, RelativePath: strings.TrimPrefix(relPath, "/"), IPAddress: s.proxy.ClientIP(r),
		UserAgent: r.UserAgent(), Status: audit.StatusSuccess})
	if err := plan.Write(w); err != nil {
		s.logger.Error("流式打包分享目录失败", "share", key, "path", relPath, "err", err)
	}
}

func shareSessionToken(r *http.Request, shareKey string) string {
	cookie, err := r.Cookie(shares.AccessCookieName(shareKey))
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (s *Server) writeShareError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, shares.ErrNotFound), errors.Is(err, files.ErrNotFound):
		WriteError(w, r, CodeFileNotFound, "分享目标不存在", nil)
	case errors.Is(err, shares.ErrForbidden):
		WriteError(w, r, CodeForbidden, err.Error(), nil)
	case errors.Is(err, shares.ErrPasswordLength), errors.Is(err, shares.ErrExpiry), errors.Is(err, shares.ErrDownloadLimit), errors.Is(err, files.ErrInvalid):
		WriteError(w, r, CodeValidationError, err.Error(), nil)
	default:
		WriteError(w, r, CodeInternalError, "分享操作失败", nil)
	}
}

func (s *Server) writePublicShareError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, shares.ErrLocked):
		WriteError(w, r, CodeUnauthorized, "请先输入访问密码", nil)
	case errors.Is(err, shares.ErrExpired):
		// 过期与次数用完是访客需要理解的状态，不再伪装成 404（docs/design/share-preview-system.md）。
		WriteError(w, r, CodeShareExpired, "分享已过期", nil)
	case errors.Is(err, shares.ErrExhausted):
		WriteError(w, r, CodeShareExhausted, "分享下载次数已用完", nil)
	default:
		// 其余情况统一 404：不区分禁用来源、真实文件缺失或已撤销。
		WriteError(w, r, CodeFileNotFound, "分享不存在或已失效", nil)
	}
}
