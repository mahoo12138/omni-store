package httpserver

import (
	"errors"
	"net/http"
	"path"
	"strconv"

	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/publicdisk"
)

// --- 公开网盘（2.0：/public 固定映射 Site Public Drive 绑定的唯一 Source） ---

// handlePublicSummary 返回公开盘是否可用及其源摘要（匿名可访问）。
func (s *Server) handlePublicSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.public.Summary()
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询公开网盘状态失败", nil)
		return
	}
	WriteData(w, r, summary)
}

func (s *Server) handlePublicBrowse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))

	result, err := s.public.List(q.Get("path"), files.ListOptions{
		Page: page, PageSize: pageSize,
		Sort: q.Get("sort"), Order: q.Get("order"),
	})
	if err != nil {
		if errors.Is(err, publicdisk.ErrNotFound) {
			WriteError(w, r, CodeFileNotFound, "公开路径不存在", nil)
			return
		}
		writeFileError(w, r, err)
		return
	}
	WriteData(w, r, result)
}

// handlePublicRaw 处理 GET /public/raw/{path...}：公开文件原始访问。
// 默认 inline，?download=1 强制下载；缓存 5 分钟（README §13.10/§13.11）。
func (s *Server) handlePublicRaw(w http.ResponseWriter, r *http.Request) {
	relPath := r.PathValue("path")
	src, inner, err := s.public.Resolve(relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, info, unlock, err := s.public.Files().OpenForReadContext(r.Context(), src, inner)
	if err != nil {
		// 公开侧不区分错误类型，统一 404，避免泄露内部信息。
		http.NotFound(w, r)
		return
	}
	defer unlock()
	defer f.Close()

	filename := sanitizeFilename(path.Base("/" + inner))
	if err := setUserContentHeaders(w, f, filename, r.URL.Query().Get("download") == "1"); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	serveContentWithDeadline(w, r, info.Name(), info.ModTime(), f)
}
