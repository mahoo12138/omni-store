package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SourceRoot 封装 Go 1.25+ 的 *os.Root，将存储源内所有文件系统访问固定在根目录 fd 内，
// 消除路径检查（Lstat）与使用（Open/Rename/Remove/Mkdir）之间的 symlink 逃逸 TOCTOU。
// 所有传入的 relPath 必须是 NormalizeRelPath 输出（以 "/" 分隔，"" 表示根目录）。
type SourceRoot struct {
	root     *os.Root
	rootPath string
	// beforeUseHook 仅供竞态测试在 check 与 use 之间注入并发 symlink 替换。
	beforeUseHook func(relPath string)
}

// OpenSourceRoot 为指定存储源根目录打开句柄受限的 *os.Root。
func OpenSourceRoot(rootPath string) (*SourceRoot, error) {
	clean := filepath.Clean(rootPath)
	r, err := os.OpenRoot(clean)
	if err != nil {
		return nil, fmt.Errorf("打开存储源根目录失败: %w", err)
	}
	return &SourceRoot{root: r, rootPath: clean}, nil
}

// Close 关闭底层 *os.Root 目录句柄。
func (r *SourceRoot) Close() error {
	if r == nil || r.root == nil {
		return nil
	}
	return r.root.Close()
}

// RootPath 返回该句柄绑定的根目录路径。
func (r *SourceRoot) RootPath() string {
	return r.rootPath
}

// SetBeforeUseHook 仅供测试在路径段预检与实际 syscall 之间注入并发替换。
func (r *SourceRoot) SetBeforeUseHook(fn func(relPath string)) {
	r.beforeUseHook = fn
}

func toRootName(relPath string) string {
	if relPath == "" {
		return "."
	}
	return filepath.FromSlash(relPath)
}

// mapRootError 将 *os.Root 的越界错误与内核 O_NOFOLLOW 的 ELOOP 统一归类为 ErrSymlink。
func mapRootError(err error) error {
	if err == nil {
		return nil
	}
	if isNoFollowErr(err) {
		return ErrSymlink
	}
	// Go 标准库 os.Root 在检测到软链接逃出根或绝对软链接时返回含 "escapes from parent" 的 PathError。
	if strings.Contains(err.Error(), "escapes from parent") {
		return ErrSymlink
	}
	return err
}

// rejectSymlinkSegments 在 *os.Root 内逐段 Lstat，拒绝已存在的任意 symlink（含源内软链接）。
func (r *SourceRoot) rejectSymlinkSegments(relPath string) error {
	if relPath == "" {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(relPath, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur += "/" + seg
		}
		info, err := r.root.Lstat(filepath.FromSlash(cur))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return mapRootError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
	}
	return nil
}

func (r *SourceRoot) runBeforeUse(relPath string) {
	if r.beforeUseHook != nil {
		r.beforeUseHook(relPath)
	}
}

// Lstat 在源根内执行 Lstat（不跟随最终软链接），并校验祖先段不包含软链接。
func (r *SourceRoot) Lstat(relPath string) (os.FileInfo, error) {
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return nil, err
	}
	r.runBeforeUse(relPath)
	info, err := r.root.Lstat(toRootName(relPath))
	if err != nil {
		return nil, mapRootError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	return info, nil
}

// LstatRaw 直接在源根内 Lstat（供 List/Stat 需要识别 TypeUnsupported 软链接本身时使用），
// 仅拒绝祖先目录为软链接，允许返回最终条目为 symlink 的 FileInfo。
func (r *SourceRoot) LstatRaw(relPath string) (os.FileInfo, error) {
	if relPath != "" {
		parent := path.Dir(relPath)
		if parent != "." && parent != "" {
			if err := r.rejectSymlinkSegments(parent); err != nil {
				return nil, err
			}
		}
	}
	r.runBeforeUse(relPath)
	info, err := r.root.Lstat(toRootName(relPath))
	if err != nil {
		return nil, mapRootError(err)
	}
	return info, nil
}

// OpenNoFollow 在源根内打开普通文件，阻断 check-to-use 软链接逃逸与源内软链接替换。
// *os.Root 在内核层杜绝一切越根逃逸；打开后结合 Lstat + os.SameFile 二次校验，
// 进一步封堵源内软链接替换（os.Root 本身允许跟随源内软链接）。
func (r *SourceRoot) OpenNoFollow(relPath string) (*os.File, os.FileInfo, error) {
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return nil, nil, err
	}
	r.runBeforeUse(relPath)
	f, err := r.root.OpenFile(toRootName(relPath), os.O_RDONLY|openNoFollowFlag, 0)
	if err != nil {
		return nil, nil, mapRootError(err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	linfo, err := r.root.Lstat(toRootName(relPath))
	if err != nil {
		_ = f.Close()
		return nil, nil, mapRootError(err)
	}
	if linfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, linfo) {
		_ = f.Close()
		return nil, nil, ErrSymlink
	}
	return f, info, nil
}

// OpenFileNoFollow 在源根内以 O_NOFOLLOW 打开或创建文件，同时拒绝源内与源外软链接。
func (r *SourceRoot) OpenFileNoFollow(relPath string, flag int, perm os.FileMode) (*os.File, error) {
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return nil, err
	}
	r.runBeforeUse(relPath)
	f, err := r.root.OpenFile(toRootName(relPath), flag|openNoFollowFlag, perm)
	if err != nil {
		return nil, mapRootError(err)
	}
	info, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return nil, statErr
	}
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		_ = f.Close()
		if flag&os.O_CREATE != 0 {
			_ = r.root.Remove(toRootName(relPath))
		}
		return nil, err
	}
	linfo, lerr := r.root.Lstat(toRootName(relPath))
	if lerr != nil {
		_ = f.Close()
		return nil, mapRootError(lerr)
	}
	if linfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, linfo) {
		_ = f.Close()
		return nil, ErrSymlink
	}
	return f, nil
}

// ReadDir 在源根内打开目录并读取目录项，拒绝源内与源外软链接。
func (r *SourceRoot) ReadDir(relPath string) ([]os.DirEntry, error) {
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return nil, err
	}
	r.runBeforeUse(relPath)
	f, err := r.root.OpenFile(toRootName(relPath), os.O_RDONLY|openNoFollowFlag, 0)
	if err != nil {
		return nil, mapRootError(err)
	}
	defer f.Close()
	if relPath != "" {
		info, statErr := f.Stat()
		if statErr != nil {
			return nil, statErr
		}
		if err := r.rejectSymlinkSegments(relPath); err != nil {
			return nil, err
		}
		linfo, lerr := r.root.Lstat(toRootName(relPath))
		if lerr != nil {
			return nil, mapRootError(lerr)
		}
		if linfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, linfo) {
			return nil, ErrSymlink
		}
	}
	return f.ReadDir(-1)
}

// Mkdir 在源根内创建单层目录。
func (r *SourceRoot) Mkdir(relPath string, perm os.FileMode) error {
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return err
	}
	r.runBeforeUse(relPath)
	return mapRootError(r.root.Mkdir(toRootName(relPath), perm))
}

// MkdirAll 在源根内逐段创建目录，拒绝任意已有段为 symlink。
func (r *SourceRoot) MkdirAll(relPath string, perm os.FileMode) error {
	if relPath == "" {
		return nil
	}
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return err
	}
	r.runBeforeUse(relPath)
	return mapRootError(r.root.MkdirAll(toRootName(relPath), perm))
}

// Rename 在源根内原子重命名路径，内核保证两端都不会通过软链接逃逸源根。
func (r *SourceRoot) Rename(oldRel, newRel string) error {
	if err := r.rejectSymlinkSegments(oldRel); err != nil {
		return err
	}
	if err := r.rejectSymlinkSegments(newRel); err != nil {
		return err
	}
	r.runBeforeUse(newRel)
	return mapRootError(r.root.Rename(toRootName(oldRel), toRootName(newRel)))
}

// Remove 在源根内删除单个文件或空目录。
func (r *SourceRoot) Remove(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("不能删除存储源根目录")
	}
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return err
	}
	r.runBeforeUse(relPath)
	return mapRootError(r.root.Remove(toRootName(relPath)))
}

// RemoveAll 在源根内递归删除路径（*os.Root.RemoveAll 不会跟随软链接逃出根）。
func (r *SourceRoot) RemoveAll(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("不能删除存储源根目录")
	}
	if err := r.rejectSymlinkSegments(relPath); err != nil {
		return err
	}
	r.runBeforeUse(relPath)
	return mapRootError(r.root.RemoveAll(toRootName(relPath)))
}

// SyncDir 在源根内以 O_NOFOLLOW 打开目录并执行 fsync。
func (r *SourceRoot) SyncDir(relDir string) error {
	if relDir == "." {
		relDir = ""
	}
	if err := r.rejectSymlinkSegments(relDir); err != nil {
		return err
	}
	f, err := r.root.OpenFile(toRootName(relDir), os.O_RDONLY|openNoFollowFlag, 0)
	if err != nil {
		return mapRootError(err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
