package security

import (
	"errors"
	"fmt"
	"strings"
)

// ErrReservedName 表示路径使用了 OmniStore 恢复流程保留的内部名称。
var ErrReservedName = errors.New("路径包含 OmniStore 保留名称，请先重命名")

// ErrManagedNamespace 表示路径命中系统托管命名空间。该命名空间只能由
// 受信任的 Site Capability 服务访问，通用入口一律按“资源不存在”处理。
var ErrManagedNamespace = errors.New("路径包含系统托管命名空间")

// ManagedNamespaceSegment 是每个存储源根下唯一的系统托管目录段。
// 系统服务（如图床）的内部数据保存在它的固定子目录中。
const ManagedNamespaceSegment = ".omnistore"

// IsReservedName 判断单个名称是否属于 OmniStore 内部操作命名空间（大小写不敏感）。
// 外部已存在的同名文件仍是用户数据，但不能经 API 新建、写入或作为移动目标。
func IsReservedName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, ".omnistore-upload-") ||
		strings.HasPrefix(lower, ".omnistore-copy-")
}

// IsInternalSegment 统一判断单个路径段是否属于 OmniStore 内部保留/托管/探针段
// （大小写不敏感），供排除规则、目录枚举与路径安全检查统一复用。
func IsInternalSegment(name string) bool {
	if IsManagedNamespaceName(name) || IsReservedName(name) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), ".omnistore-write-test-")
}

// IsManagedNamespaceName 判断单个名称是否为托管命名空间目录本身。
// 必须整段精确匹配：`.omnistore.txt` 等普通名字不受影响。大小写变体一并
// 拒绝，因为宿主文件系统的大小写敏感性不可依赖，fail-closed 优先。
func IsManagedNamespaceName(name string) bool {
	return strings.EqualFold(name, ManagedNamespaceSegment)
}

// ContainsManagedNamespace 判断相对路径的任意一段是否为托管命名空间。
func ContainsManagedNamespace(relPath string) bool {
	for _, segment := range strings.Split(relPath, "/") {
		if IsManagedNamespaceName(segment) {
			return true
		}
	}
	return false
}

// ValidateUserRelPath 拒绝用户路径任意一段使用托管命名空间或内部保留名称。
func ValidateUserRelPath(relPath string) error {
	for _, segment := range strings.Split(relPath, "/") {
		if IsManagedNamespaceName(segment) {
			return fmt.Errorf("%w: %s", ErrManagedNamespace, segment)
		}
		if IsReservedName(segment) {
			return fmt.Errorf("%w: %s", ErrReservedName, segment)
		}
	}
	return nil
}

// NormalizeRelPath 规范化存储源内部相对路径（README §10.8 / §28）。
// 输入形如 "/2026/travel" 或 "2026/travel"，输出统一为不带前导斜杠、
// 以 "/" 分隔的相对路径；根目录返回 ""。
// 拒绝：路径穿越、空字节、控制字符、单段内的 "." / ".."、仅空白的文件名。
func NormalizeRelPath(input string) (string, error) {
	p := strings.ReplaceAll(input, "\\", "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}

	for _, r := range p {
		if r < 32 {
			return "", fmt.Errorf("路径包含非法控制字符")
		}
	}

	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			continue // 折叠连续斜杠
		}
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("路径不允许包含 . 或 ..")
		}
		if strings.TrimSpace(seg) == "" {
			return "", fmt.Errorf("文件名不能仅由空白组成")
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/"), nil
}

// ValidateFileName 校验单个文件/目录名（README §10.8）。
func ValidateFileName(name string) error {
	if name == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("文件名不能为空")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("文件名不能是 . 或 ..")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("文件名不能包含斜杠")
	}
	if IsManagedNamespaceName(name) {
		return fmt.Errorf("%w: %s", ErrManagedNamespace, name)
	}
	if IsReservedName(name) {
		return fmt.Errorf("%w: %s", ErrReservedName, name)
	}
	for _, r := range name {
		if r < 32 {
			return fmt.Errorf("文件名包含非法控制字符")
		}
	}
	return nil
}
