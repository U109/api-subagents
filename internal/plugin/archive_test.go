package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// updateArchiveFixture 生成只含合成内容的 ZIP，可定点构造重复项、越界路径、符号链接和压缩后超限文件。
func updateArchiveFixture(t *testing.T, kind string) []byte {
	t.Helper()
	files := fixturePayload()
	files[".mcp.json"].Data = []byte(`{"mcpServers":{"api-subagents":{"command":"./bin/api-subagents-worker.exe","args":[],"cwd":"."}}}`)
	if kind == "foreign-command" {
		files[".mcp.json"].Data = []byte(`{"mcpServers":{"api-subagents":{"command":"powershell.exe","cwd":"."}}}`)
	}
	if kind == "oversize" {
		files["Install.cmd"].Data = bytes.Repeat([]byte("x"), 2*1024*1024+1)
	}
	if kind == "missing" {
		delete(files, "Install.cmd")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range names {
		path := name
		if kind == "traversal" && name == "Install.cmd" {
			path = "../Install.cmd"
		}
		if kind == "duplicate" && name == "Install.cmd" {
			path = "Configure.cmd"
		}
		header := &zip.FileHeader{Name: path, Method: zip.Deflate}
		header.SetMode(0644)
		if kind == "symlink" && name == "Install.cmd" {
			header.SetMode(os.ModeSymlink | 0777)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(files[name].Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// TestUpdateArchiveBoundaries 拒绝不完整或越界更新包；合法包保留个人配置并沿用独立 Worker 安装目录。
func TestUpdateArchiveBoundaries(t *testing.T) {
	for _, kind := range []string{"complete", "traversal", "duplicate", "missing", "symlink", "oversize", "foreign-command", "version-mismatch", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			data := updateArchiveFixture(t, kind)
			expected := "0.3.0"
			if kind == "version-mismatch" {
				expected = "0.4.0"
			}
			if kind == "truncated" {
				data = data[:len(data)/2]
			}
			source, err := OpenUpdateArchive(data, expected)
			if kind != "complete" {
				if err == nil {
					t.Fatal("unsafe update archive accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			opts := InstallOptions{ProfileRoot: t.TempDir(), DataRoot: t.TempDir(), CodexHome: t.TempDir(), SkipCodex: true}
			t.Setenv("CODEX_HOME", opts.CodexHome)
			configPath := filepath.Join(opts.DataRoot, "models.json")
			if err = shared.AtomicWrite(configPath, []byte("user-config-sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := InstallPlugin(context.Background(), source, opts)
			if err != nil || result.Installed {
				t.Fatal(err)
			}
			if actual, _ := os.ReadFile(configPath); string(actual) != "user-config-sentinel" {
				t.Fatal("plugin update changed model configuration")
			}
			if actual, _ := fs.ReadFile(source, "bin/api-subagents-worker.exe"); !bytes.HasPrefix(actual, []byte("MZ")) {
				t.Fatal("worker missing")
			}
		})
	}
}
