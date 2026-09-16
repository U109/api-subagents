package shared

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Inside 用相对路径判断目录边界，避免同前缀的另一个目录被误认为项目内。
func Inside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// SamePath 统一 Windows 路径大小写，比较经过规范化的完整项目目录。
func SamePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Contains 在显式路径选择中检查成员，不使用可能扩大写入范围的通配符。
func Contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// SafeFileError 隐去底层操作的绝对路径，模型只需要相对文件与错误类别。
func SafeFileError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Sprintf("文件操作失败：%v", pe.Err)
	}
	return err.Error()
}
