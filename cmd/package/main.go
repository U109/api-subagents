// 构建工具只读取声明的源码、编译产物与依赖许可证，不扫描用户配置目录。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pluginruntime "github.com/U109/api-subagents/internal/plugin"
	shared "github.com/U109/api-subagents/internal/shared"
	updates "github.com/U109/api-subagents/internal/updates"
	workfiles "github.com/U109/api-subagents/internal/workspace"
)

type module struct{ Path, Version, Dir string }

// notices 收集两个运行程序实际引用模块的许可证，不把开发工具的全部依赖塞入安装包。
func notices() error {
	modules := map[string]module{}
	licenseDirs := map[string]map[string]bool{}
	for _, target := range []string{"./cmd/worker", "."} {
		cmd := exec.Command("go", "list", "-mod=mod", "-deps", "-json", "-tags", "desktop,production", target)
		output, err := cmd.Output()
		if err != nil {
			return errors.New("无法列出运行依赖，请先完成 go mod download。")
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var item struct {
				Module *module
				Dir    string
			}
			if err := decoder.Decode(&item); err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			if item.Module != nil && item.Module.Path != "github.com/U109/api-subagents" {
				modules[item.Module.Path] = *item.Module
				if licenseDirs[item.Module.Path] == nil {
					licenseDirs[item.Module.Path] = map[string]bool{}
				}
				// 部分模块在子包中采用独立许可证，例如 WebView2 的 Go loader。
				for dir := item.Dir; shared.Inside(item.Module.Dir, dir); dir = filepath.Dir(dir) {
					licenseDirs[item.Module.Path][dir] = true
					if shared.SamePath(dir, item.Module.Dir) {
						break
					}
				}
			}
		}
	}
	paths := []string{}
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var text strings.Builder
	text.WriteString("API Subagents — Third-party notices\n\nThis file records third-party licenses; it does not license the project itself.\nWebView2 is supplied by Microsoft as a shared system runtime.\n\n")
	goRoot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return err
	}
	goLicense, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goRoot)), "LICENSE"))
	if err != nil {
		return err
	}
	text.WriteString("===== Go standard library and runtime =====\n")
	text.Write(goLicense)
	text.WriteString("\n")
	for _, path := range paths {
		m := modules[path]
		licensePaths := []string{}
		for dir := range licenseDirs[path] {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				name := strings.ToUpper(entry.Name())
				if !entry.IsDir() && (strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING") || strings.HasPrefix(name, "NOTICE")) {
					licensePaths = append(licensePaths, filepath.Join(dir, entry.Name()))
				}
			}
		}
		sort.Strings(licensePaths)
		found := false
		for _, licensePath := range licensePaths {
			data, err := os.ReadFile(licensePath)
			if err != nil {
				return err
			}
			relative, _ := filepath.Rel(m.Dir, licensePath)
			text.WriteString("\n===== " + m.Path + " " + m.Version + " / " + filepath.ToSlash(relative) + " =====\n")
			text.Write(data)
			text.WriteString("\n")
			found = true
		}
		if !found {
			return fmt.Errorf("运行依赖缺少许可证：%s", m.Path)
		}
	}
	return os.WriteFile("THIRD-PARTY-NOTICES.txt", []byte(strings.TrimRight(text.String(), "\r\n")+"\n"), 0644)
}

// stage 用明确清单创建可重复的插件 ZIP，路径和凭据文件检查失败即停止打包。
func stage() error {
	files := append([]string{}, pluginruntime.PluginFiles...)
	files = append(files, "bin/api-subagents-worker.exe")
	sort.Strings(files)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range files {
		if workfiles.Blocked(name) {
			return fmt.Errorf("禁止发布的路径：%s", name)
		}
		source := name
		if name == "bin/api-subagents-worker.exe" {
			source = "build/bin/api-subagents-worker.exe"
		}
		data, err := os.ReadFile(filepath.FromSlash(source))
		if err != nil {
			return err
		}
		if name == ".mcp.json" {
			var value shared.Object
			if json.Unmarshal(data, &value) != nil {
				return errors.New("MCP 模板无效")
			}
			server := shared.Obj(shared.Obj(value["mcpServers"])["api-subagents"])
			if server["command"] != "./bin/api-subagents-worker.exe" || server["env"] != nil {
				return errors.New("MCP 模板不能包含本机命令或凭据。")
			}
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		header.SetMode(0644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = entry.Write(data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return shared.AtomicWrite("bundle/payload.zip", buffer.Bytes(), 0600)
}

// metadata 为 Go 更新器和旧 Electron 客户端同时生成校验清单，旧版可完整下载安装器。
func metadata() error {
	var release updates.Release
	data, err := os.ReadFile("packaging/release.json")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &release); err != nil {
		return err
	}
	name := "API-Subagents-Setup-" + release.Version + "-x64.exe"
	installer, err := os.ReadFile(filepath.Join("release", name))
	if err != nil {
		return err
	}
	manifest := updates.UpdateManifest{Version: release.Version, File: name, SHA256: shared.Hash(installer), Size: int64(len(installer))}
	if err = shared.AtomicWrite("release/update.json", append(shared.Marshal(manifest), '\n'), 0644); err != nil {
		return err
	}
	sha := sha512.Sum512(installer)
	shaText := base64.StdEncoding.EncodeToString(sha[:])
	yml := fmt.Sprintf("version: %s\nfiles:\n  - url: %s\n    sha512: %s\n    size: %d\npath: %s\nsha512: %s\nreleaseDate: '%s'\n", release.Version, name, shaText, len(installer), name, shaText, time.Now().UTC().Format(time.RFC3339))
	return shared.AtomicWrite("release/latest.yml", []byte(yml), 0644)
}

// pluginMetadata 单独发布已经按白名单生成的插件包，版本来自插件自身清单，不要求与 App 同步升级。
func pluginMetadata() error {
	var release updates.Release
	data, err := os.ReadFile("packaging/release.json")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &release); err != nil {
		return err
	}
	version, err := pluginruntime.PayloadVersion(os.DirFS("."))
	if err != nil {
		return err
	}
	payload, err := os.ReadFile("bundle/payload.zip")
	if err != nil {
		return err
	}
	if _, err = pluginruntime.OpenUpdateArchive(payload, version); err != nil {
		return err
	}
	if !updates.NewerVersion(release.PluginMinAppVersion, "0.0.0") || updates.NewerVersion(release.PluginMinAppVersion, release.Version) {
		return errors.New("插件最低 App 版本配置无效。")
	}
	name := "api-subagents-plugin-" + version + "-windows-amd64.zip"
	if err = shared.AtomicWrite(filepath.Join("release", name), payload, 0644); err != nil {
		return err
	}
	manifest := updates.UpdateManifest{Version: version, File: name, SHA256: shared.Hash(payload), Size: int64(len(payload)), FormatVersion: 1, MinAppVersion: release.PluginMinAppVersion}
	return shared.AtomicWrite("release/plugin-update.json", append(shared.Marshal(manifest), '\n'), 0644)
}

// verify 核对实际嵌入包的文件集合和模型配置模板，拒绝多余文件进入发布。
func verify() error {
	reader, err := zip.OpenReader("bundle/payload.zip")
	if err != nil {
		return err
	}
	defer reader.Close()
	allowed := map[string]bool{"bin/api-subagents-worker.exe": true}
	for _, path := range pluginruntime.PluginFiles {
		allowed[path] = true
	}
	for _, file := range reader.File {
		if !allowed[file.Name] || workfiles.Blocked(file.Name) {
			return fmt.Errorf("安装包含非白名单文件：%s", file.Name)
		}
		delete(allowed, file.Name)
	}
	if len(allowed) > 0 {
		return errors.New("安装包缺少运行文件。")
	}
	fmt.Printf("Verified %d payload files; local configuration excluded.\n", len(reader.File))
	return nil
}

// main 提供可独立验证的构建步骤，任何步骤失败都阻止后续发布。
func main() {
	var err error
	if len(os.Args) != 2 {
		err = errors.New("用法：package notices|stage|metadata|plugin-metadata|verify")
	} else {
		switch os.Args[1] {
		case "notices":
			err = notices()
		case "stage":
			err = stage()
		case "metadata":
			err = metadata()
		case "plugin-metadata":
			err = pluginMetadata()
		case "verify":
			err = verify()
		default:
			err = errors.New("未知构建步骤。")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
