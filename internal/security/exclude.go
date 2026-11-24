package security

import (
	"path"
	"strings"
)

// 系统强制排除规则，永远生效，管理员不能关闭（README §11.2）。
// `.omnistore` 托管命名空间按目录段整体排除（含根级与任意嵌套层级），
// 让搜索重过滤、递归打包等 matcher 消费方统一隐藏内部子树。
var forcedExcludePatterns = []string{
	".omnistore",
	"**/.omnistore",
	".omnistore/**",
	"**/.omnistore/**",
	".omnistore-upload-*",
	"**/.omnistore-upload-*",
	".omnistore-copy-*",
	"**/.omnistore-copy-*",
	".omnistore-write-test-*",
	"**/.omnistore-write-test-*",
}

// ExcludeMatcher 按简单 glob 语法匹配排除路径（README §11.1）。
// 支持 ** 跨目录通配；不支持正则、否定规则。
type ExcludeMatcher struct {
	patterns [][]string
}

// NewExcludeMatcher 编译排除规则（自动附加系统强制规则）。
func NewExcludeMatcher(patterns []string) *ExcludeMatcher {
	m := &ExcludeMatcher{}
	for _, p := range append(append([]string{}, forcedExcludePatterns...), patterns...) {
		p = strings.Trim(strings.TrimSpace(strings.ReplaceAll(p, "\\", "/")), "/")
		if p == "" {
			continue
		}
		m.patterns = append(m.patterns, strings.Split(p, "/"))
	}
	return m
}

// Match 判断相对路径是否命中任一排除规则。
// relPath 必须是 NormalizeRelPath 的输出。
// 系统内部段（.omnistore、.omnistore-upload-*、.omnistore-copy-*、.omnistore-write-test-*）
// 始终按大小写不敏感方式强制排除，防止大小写不敏感文件系统上的变体绕过。
func (m *ExcludeMatcher) Match(relPath string) bool {
	if relPath == "" {
		return false
	}
	segs := strings.Split(relPath, "/")
	for _, seg := range segs {
		if IsInternalSegment(seg) {
			return true
		}
	}
	for _, pat := range m.patterns {
		if matchSegments(pat, segs) {
			return true
		}
	}
	return false
}

// matchSegments 递归匹配模式段与路径段。
// "**" 匹配零个或多个路径段；其余段使用 path.Match 单段匹配。
func matchSegments(pattern, segs []string) bool {
	if len(pattern) == 0 {
		return len(segs) == 0
	}
	if pattern[0] == "**" {
		// ** 匹配零段或吞掉一段后继续。
		if matchSegments(pattern[1:], segs) {
			return true
		}
		if len(segs) > 0 {
			return matchSegments(pattern, segs[1:])
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], segs[1:])
}

// MatchPrefix 判断路径本身或其任一父级目录是否命中排除规则。
// 用于阻止访问被排除目录内部的内容（例如规则 private/**
// 时，private/a/b.txt 的父级 private/a 已命中）。
func (m *ExcludeMatcher) MatchPrefix(relPath string) bool {
	if relPath == "" {
		return false
	}
	segs := strings.Split(relPath, "/")
	for i := 1; i <= len(segs); i++ {
		if m.Match(strings.Join(segs[:i], "/")) {
			return true
		}
	}
	return false
}
