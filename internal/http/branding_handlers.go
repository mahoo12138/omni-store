package httpserver

import (
	"errors"
	"net/http"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/operations"
)

// instanceName 返回当前实例显示名称，未配置时回退默认值。
func (s *Server) instanceName() string {
	return s.operations.InstanceName()
}

func (s *Server) handleAdminGetBranding(w http.ResponseWriter, r *http.Request) {
	WriteData(w, r, map[string]any{"instance_name": s.instanceName()})
}

func (s *Server) handleAdminSetBranding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceName string `json:"instance_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := s.operations.SetInstanceName(req.InstanceName)
	if err != nil {
		if errors.Is(err, operations.ErrInstanceNameTooLong) {
			WriteError(w, r, CodeValidationError, err.Error(), nil)
			return
		}
		WriteError(w, r, CodeInternalError, "保存实例名称失败", nil)
		return
	}
	s.adminAudit(r, "update_branding", audit.StatusSuccess, "")
	WriteData(w, r, map[string]any{"instance_name": name})
}
