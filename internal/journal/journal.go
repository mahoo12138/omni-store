// Package journal 提供崩溃恢复日志的共享原子落盘、严格反序列化、阶段标记与孤儿清理原语，
// 供 files / imagebed / s3api 8 套恢复日志共用，消除重复实现并统一安全硬化标准。
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// SyncDir 将目录元数据变更落盘（Windows 不支持目录 fsync 时静默跳过）。
func SyncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if runtime.GOOS == "windows" {
		return closeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// WriteJSONExclusive 原子独占写入 JSON 操作日志：
// temp(.operation-*.tmp) + chmod(0600) + encode + fsync + close + hardlink + remove temp + dir fsync。
func WriteJSONExclusive[T any](dir, targetPath string, op T) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(targetPath); err == nil {
		return fmt.Errorf("操作日志 %s 尚未恢复", filepath.Base(targetPath))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(dir, ".operation-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keepTemp := true
	defer func() {
		_ = temp.Close()
		if keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if err := json.NewEncoder(temp).Encode(op); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, targetPath); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	keepTemp = false
	return SyncDir(dir)
}

// DecodeStrictJSON 严格解析单个 JSON 值：拒绝未知字段与尾部冗余数据。
func DecodeStrictJSON[T any](reader io.Reader) (T, error) {
	var op T
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&op); err != nil {
		return op, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return op, fmt.Errorf("操作日志包含额外 JSON 值")
		}
		return op, err
	}
	return op, nil
}

// ReadStrictJSON 拒绝符号链接并严格解析 JSON 操作日志。
func ReadStrictJSON[T any](absPath string) (T, error) {
	var zero T
	info, err := os.Lstat(absPath)
	if err != nil {
		return zero, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return zero, fmt.Errorf("操作日志 %s 不是普通文件", filepath.Base(absPath))
	}
	handle, err := os.Open(absPath)
	if err != nil {
		return zero, err
	}
	op, decodeErr := DecodeStrictJSON[T](handle)
	closeErr := handle.Close()
	if decodeErr != nil || closeErr != nil {
		return zero, errors.Join(decodeErr, closeErr)
	}
	return op, nil
}

// CreateMarker 幂等创建并持久化阶段标记文件（0600 + fsync + dir fsync）。
func CreateMarker(markerPath, dir string) error {
	handle, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		exists, checkErr := MarkerExists(markerPath)
		if checkErr != nil {
			return checkErr
		}
		if !exists {
			return fmt.Errorf("阶段标记 %s 不存在", filepath.Base(markerPath))
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		_ = os.Remove(markerPath)
		return err
	}
	if err := handle.Close(); err != nil {
		_ = os.Remove(markerPath)
		return err
	}
	return SyncDir(dir)
}

// MarkerExists 校验阶段标记是否为真实普通文件（拒绝符号链接）。
func MarkerExists(markerPath string) (bool, error) {
	info, err := os.Lstat(markerPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("阶段标记 %s 不是普通文件", filepath.Base(markerPath))
	}
	return true, nil
}

// RemoveFiles 幂等删除一组日志与阶段标记并同步目录。
func RemoveFiles(dir string, paths ...string) error {
	for _, item := range paths {
		if item == "" {
			continue
		}
		if err := os.Remove(item); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := SyncDir(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// CleanOrphans 清理中断残留的 .operation-*.tmp 临时文件与失去主 .json 日志的孤儿阶段标记。
func CleanOrphans(dir string, markerSuffixes ...string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".operation-") && strings.HasSuffix(name, ".tmp") {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		for _, suffix := range markerSuffixes {
			if !strings.HasSuffix(name, suffix) {
				continue
			}
			opID := strings.TrimSuffix(name, suffix)
			if _, statErr := os.Stat(filepath.Join(dir, opID+".json")); errors.Is(statErr, fs.ErrNotExist) {
				if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			} else if statErr != nil {
				return statErr
			}
		}
	}
	return nil
}

// ListJSONFiles 返回按名称排序的 .json 日志文件名列表，并拒绝符号链接。
func ListJSONFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("操作日志 %s 不能是符号链接", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}
