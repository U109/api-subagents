package shared

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type Object = map[string]any

var EnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var IDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// Marshal 将受控的内部数据编码为 JSON，供协议历史和任务记录使用。
func Marshal(value any) []byte { data, _ := json.Marshal(value); return data }

// Obj 安全读取协议中的对象，缺失或类型不匹配时返回空对象。
func Obj(value any) Object {
	result, _ := value.(map[string]any)
	if result == nil {
		return Object{}
	}
	return result
}

// Arr 安全读取协议数组，缺失时返回空切片。
func Arr(value any) []any { result, _ := value.([]any); return result }

// Str 安全读取字符串，不把任意对象或服务端错误原文转换进日志。
func Str(value any) string { result, _ := value.(string); return result }

// Int 读取解码后的 JSON 整数，调用边界另行校验范围与小数。
func Int(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// Clip 按 Unicode 字符截短展示文字，避免截断中文编码。
func Clip(value string, n int) string {
	chars := []rune(value)
	if len(chars) > n {
		return string(chars[:n])
	}
	return value
}

// UUID 使用系统随机源生成不可预测的任务及临时文件标识。
func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Hash 返回内容指纹，用于并发编辑检查和独立运行文件缓存。
func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// AtomicWrite 通过同目录临时文件及替换保存，避免中断产生半份配置。
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + "." + UUID() + ".tmp"
	defer os.Remove(temp)
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, path)
}
