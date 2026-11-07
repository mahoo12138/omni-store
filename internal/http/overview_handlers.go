package httpserver

import (
	"net/http"
	"os"
	"strings"

	"github.com/omni-store/omnistore/internal/buildinfo"
	"github.com/omni-store/omnistore/internal/capabilities"
	"github.com/omni-store/omnistore/internal/operations"
)

// overviewSystem 是健康快照之外的连接配置。
type overviewSystem struct {
	HTTPAddr     string `json:"http_addr"`
	PublicURL    string `json:"public_url"`
	S3Enabled    bool   `json:"s3_enabled"`
	S3Status     string `json:"s3_status"`
	WebdavStatus string `json:"webdav_status"`
}

type overviewResponse struct {
	System              overviewSystem     `json:"system"`
	Operations          *operations.Status `json:"operations"`
	AnonymousImageBedOn bool               `json:"anonymous_image_bed_on"`
}

// handleAdminOverview 复用运维服务的全量快照，仪表盘只需一个聚合请求。
func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	status, err := s.operations.GetStatus()
	if err != nil {
		WriteError(w, r, CodeInternalError, "加载仪表盘状态失败", nil)
		return
	}
	out := overviewResponse{
		Operations: status,
		System: overviewSystem{
			HTTPAddr:     s.cfg.Server.HTTPAddr,
			PublicURL:    s.cfg.Server.PublicURL,
			S3Enabled:    s.cfg.Server.S3Enabled,
			S3Status:     ternary(s.cfg.Server.S3Enabled, "已启用", "未启用"),
			WebdavStatus: "已启用",
		},
	}
	var val string
	if err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = 'anonymous_image_bed_enabled'`).Scan(&val); err == nil {
		out.AnonymousImageBedOn = val == "1" || strings.EqualFold(val, "true")
	}
	WriteData(w, r, out)
}

func ternary(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// --- 公开系统状态（docs/home-1.png 右栏"系统状态"）---

type systemStatusFlag struct {
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
	Hint    string `json:"hint"`
}

type systemStatusResponse struct {
	S3           systemStatusFlag `json:"s3"`
	WebDAV       systemStatusFlag `json:"webdav"`
	FilePreview  systemStatusFlag `json:"file_preview"`
	Anonymous    systemStatusFlag `json:"anonymous"`
	PublicDrive  systemStatusFlag `json:"public_drive"`
	ImageBed     systemStatusFlag `json:"image_bed"`
	SSO          systemStatusFlag `json:"sso"`
	Version      string           `json:"version"`
	PublicURL    string           `json:"public_url"`
	InstanceName string           `json:"instance_name"`
}

// siteServiceStatus 描述当前绑定是否可用，状态不泄露源身份、路径或底层错误。
// 它只反映当前服务入口；已有图床文件继续使用各自记录中的源。
func (s *Server) siteServiceStatus(capability, availableHint string) systemStatusFlag {
	binding, err := s.capabilities.GetBinding(capability)
	if err != nil {
		return systemStatusFlag{Status: "暂不可用", Hint: "站点服务状态暂不可用"}
	}
	if binding.StorageSourceID == nil {
		return systemStatusFlag{Status: "未绑定", Hint: "管理员尚未配置服务存储源"}
	}
	if !binding.Enabled {
		return systemStatusFlag{Status: "已关闭", Hint: "站点服务当前未开启"}
	}
	source, err := s.sources.GetByID(*binding.StorageSourceID)
	if err != nil {
		return systemStatusFlag{Status: "存储源不可用", Hint: "绑定的存储源暂不可用"}
	}
	if source.IsDisabled {
		return systemStatusFlag{Status: "存储源已禁用", Hint: "绑定的存储源当前已禁用"}
	}
	root, err := os.Stat(source.RootPath)
	if err != nil || !root.IsDir() {
		return systemStatusFlag{Status: "目录不可用", Hint: "绑定的存储目录暂不可用"}
	}
	return systemStatusFlag{Enabled: true, Status: "已启用", Hint: availableHint}
}

// handleSystemStatus 返回系统功能与站点服务的实际可用状态。
// 不需要鉴权，只暴露开关与状态描述。
func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	out := systemStatusResponse{Version: buildinfo.Version, PublicURL: s.cfg.Server.PublicURL,
		InstanceName: s.instanceName()}

	// S3 兼容存储
	out.S3.Enabled = s.cfg.Server.S3Enabled
	out.S3.Status = ternary(s.cfg.Server.S3Enabled, "已启用", "未启用")
	out.S3.Hint = ternary(s.cfg.Server.S3Enabled, "S3 Path-style 服务运行正常", "在配置中显式启用后可使用")

	// WebDAV 服务
	out.WebDAV.Enabled = true
	out.WebDAV.Status = "已启用"
	out.WebDAV.Hint = "WebDAV 服务运行正常，可正常连接"

	// OIDC 单点登录（2.1.0）
	if ssoEnabled := s.oidcEnabled(); ssoEnabled {
		out.SSO = systemStatusFlag{Enabled: true, Status: "已启用", Hint: "支持通过 OIDC 单点登录（Casdoor）"}
	} else {
		out.SSO = systemStatusFlag{Status: "未启用", Hint: "管理员配置身份提供方后可使用 SSO 登录"}
	}

	// 文件预览服务
	out.FilePreview.Enabled = true
	out.FilePreview.Status = "已启用"
	out.FilePreview.Hint = "文件预览服务运行正常"

	out.PublicDrive = s.siteServiceStatus(capabilities.CapabilityPublicDrive, "可匿名浏览和下载公开文件")
	out.ImageBed = s.siteServiceStatus(capabilities.CapabilityImageBed, "登录用户可上传图片")

	// 匿名上传还依赖图床当前绑定可用，不能把保存的入口开关当作可上传状态。
	anonymous, err := s.imagebed.GetAnonymousSettings()
	if err != nil {
		out.Anonymous = systemStatusFlag{Status: "暂不可用", Hint: "匿名上传状态暂不可用"}
	} else if !anonymous.Enabled {
		out.Anonymous = systemStatusFlag{Status: "未启用", Hint: "匿名上传未启用"}
	} else if !out.ImageBed.Enabled {
		out.Anonymous = systemStatusFlag{Status: "暂不可用", Hint: "图床服务当前不可用，匿名上传暂不可用"}
	} else {
		out.Anonymous = systemStatusFlag{Enabled: true, Status: "已启用", Hint: "可匿名上传图片；已有图片按保留策略提供访问"}
	}

	WriteData(w, r, out)
}
