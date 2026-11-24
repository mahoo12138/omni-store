package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProbeCaseInsensitive 探测指定目录所在文件系统是否大小写不敏感。
// 优先使用无写盘的 os.SameFile 对比大小写翻转后的目录名；若目录名不含 ASCII 字母，
// 则退化为平台保守默认值（darwin/windows 视为大小写不敏感）。
func ProbeCaseInsensitive(dir string) bool {
	clean := filepath.Clean(dir)
	info, err := os.Stat(clean)
	if err != nil {
		return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	}
	base := filepath.Base(clean)
	parent := filepath.Dir(clean)
	swapped := swapASCIICase(base)
	if swapped != base && parent != clean {
		other, err := os.Stat(filepath.Join(parent, swapped))
		if err != nil {
			return false
		}
		return os.SameFile(info, other)
	}
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

func swapASCIICase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
			changed = true
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c - 'A' + 'a')
			changed = true
		default:
			b.WriteByte(c)
		}
	}
	if !changed {
		return s
	}
	return b.String()
}
