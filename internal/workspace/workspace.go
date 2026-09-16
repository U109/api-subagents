package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/U109/api-subagents/internal/shared"
)

const maxFileBytes = 200000

var excluded = map[string]bool{".git": true, ".codex": true, ".ssh": true, ".aws": true, ".azure": true, ".pal": true, "node_modules": true, ".venv": true, "venv": true, "dist": true, "build": true, "coverage": true, ".tools": true, ".runtime": true, "release": true, ".desktop-resources": true}

var secretName = regexp.MustCompile(`(?i)^\.env($|\.)|\.(pem|key|p12|pfx)$|^(credentials|auth|models|runtime)\.json$`)

type Edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type Proposal struct {
	Path        string  `json:"path"`
	OriginalSHA *string `json:"originalSha256"`
	Content     string  `json:"content"`
	Edits       []Edit  `json:"edits,omitempty"`
	Redacted    bool    `json:"redacted,omitempty"`
}

type Workspace struct {
	Root      string
	handle    *os.Root
	Proposals map[string]Proposal
	order     []string
}

type FileContent struct {
	Path    string `json:"path"`
	SHA     string `json:"sha256"`
	Content string `json:"content"`
}

// Blocked 排除路径各级的凭据、依赖和生成物，大小写变体也不能绕过。
func Blocked(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(c rune) bool { return c == '/' || c == '\\' }) {
		if excluded[strings.ToLower(part)] || secretName.MatchString(part) {
			return true
		}
	}
	return false
}

// OpenWorkspace 固定真实根目录并打开受限文件句柄，底层禁止通过链接逃出项目。
func OpenWorkspace(root string) (*Workspace, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("workspace 必须是绝对目录。")
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("无法打开项目目录。")
	}
	handle, err := os.OpenRoot(actual)
	if err != nil {
		return nil, errors.New("workspace 必须是可访问的目录。")
	}
	return &Workspace{Root: actual, handle: handle, Proposals: map[string]Proposal{}}, nil
}

// Close 释放项目目录句柄，已保存的修改建议仍保留在内存中。
func (w *Workspace) Close() { _ = w.handle.Close() }

// Changes 按提出顺序返回修改建议，避免任务层依赖文件工具内部的排序结构。
func (w *Workspace) Changes() []Proposal {
	items := make([]Proposal, 0, len(w.order))
	for _, path := range w.order {
		items = append(items, w.Proposals[path])
	}
	return items
}

// Resolve 校验相对路径、排除规则及真实链接目标；新文件沿最近存在的父目录校验。
func (w *Workspace) Resolve(relative string, allowNew bool) (string, error) {
	if strings.ContainsAny(relative, "\x00:\\") || strings.HasPrefix(relative, "/") || filepath.IsAbs(relative) {
		return "", errors.New("请使用项目内相对路径，以 / 分隔。")
	}
	target := filepath.Join(w.Root, filepath.FromSlash(relative))
	rel, _ := filepath.Rel(w.Root, target)
	if !shared.Inside(w.Root, target) || Blocked(rel) {
		return "", errors.New("路径超出项目或属于排除目录/凭据文件。")
	}
	probe := target
	for {
		actual, err := filepath.EvalSymlinks(probe)
		if err == nil {
			actualRel, _ := filepath.Rel(w.Root, actual)
			if !shared.Inside(w.Root, actual) || Blocked(actualRel) {
				return "", errors.New("符号链接指向项目外或受限文件。")
			}
			break
		}
		if !os.IsNotExist(err) || !allowNew || probe == w.Root {
			return "", err
		}
		probe = filepath.Dir(probe)
	}
	return rel, nil
}

// Read 只读取最多 200 KB 的普通 UTF-8 文本，实际读取再次限额，防止文件中途增长。
func (w *Workspace) Read(relative string) (FileContent, error) {
	rel, err := w.Resolve(relative, false)
	if err != nil {
		return FileContent{}, err
	}
	f, err := w.handle.Open(rel)
	if err != nil {
		return FileContent{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > maxFileBytes {
		return FileContent{}, errors.New("只允许读取不超过 200 KB 的普通文本文件。")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return FileContent{}, err
	}
	if len(data) > maxFileBytes {
		return FileContent{}, errors.New("文件超过 200 KB。")
	}
	if bytes.ContainsRune(data, 0) || !utf8.Valid(data) {
		return FileContent{}, errors.New("不支持二进制或非 UTF-8 文件。")
	}
	return FileContent{relative, shared.Hash(data), string(data)}, nil
}

// List 逐批读取目录，最多扫描 1500 项并返回 500 项，跳过符号链接及受限名称。
func (w *Workspace) List(relative string, recursive bool) (shared.Object, error) {
	start, err := w.Resolve(relative, false)
	if err != nil {
		return nil, err
	}
	queue := []string{start}
	entries := []any{}
	scanned := 0
	for len(queue) > 0 && len(entries) < 500 && scanned < 1500 {
		rel := queue[0]
		queue = queue[1:]
		if _, err = w.Resolve(filepath.ToSlash(rel), false); err != nil {
			return nil, err
		}
		dir, err := w.handle.Open(rel)
		if err != nil {
			return nil, err
		}
		for {
			items, readErr := dir.ReadDir(32)
			for _, item := range items {
				scanned++
				path := filepath.Join(rel, item.Name())
				if !Blocked(path) && item.Type()&os.ModeSymlink == 0 {
					kind := "file"
					if item.IsDir() {
						kind = "directory"
						if recursive {
							queue = append(queue, path)
						}
					}
					entries = append(entries, shared.Object{"path": filepath.ToSlash(path), "type": kind})
				}
				if len(entries) >= 500 || scanned >= 1500 {
					break
				}
			}
			if readErr != nil || len(entries) >= 500 || scanned >= 1500 {
				break
			}
		}
		dir.Close()
		if !recursive {
			break
		}
	}
	return shared.Object{"entries": entries, "truncated": len(entries) >= 500 || scanned >= 1500}, nil
}

// Search 做字面量、忽略大小写的文本搜索，最多返回 80 处匹配及行号。
func (w *Workspace) Search(query, relative string) (shared.Object, error) {
	if query == "" || len([]rune(query)) > 300 {
		return nil, errors.New("搜索词须为 1–300 字符。")
	}
	listing, err := w.List(relative, true)
	if err != nil {
		return nil, err
	}
	matches := []any{}
	scanned := 0
	for _, v := range shared.Arr(listing["entries"]) {
		entry := shared.Obj(v)
		if entry["type"] != "file" {
			continue
		}
		file, err := w.Read(shared.Str(entry["path"]))
		if err != nil {
			continue
		}
		scanned++
		for index, line := range strings.Split(file.Content, "\n") {
			if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				matches = append(matches, shared.Object{"path": file.Path, "line": index + 1, "text": shared.Clip(strings.TrimSuffix(line, "\r"), 400)})
				if len(matches) >= 80 {
					return shared.Object{"matches": matches, "truncated": true, "scannedFiles": scanned}, nil
				}
			}
		}
	}
	return shared.Object{"matches": matches, "truncated": listing["truncated"], "scannedFiles": scanned}, nil
}

// SameSHA 区分新文件的 null 指纹与已有文件的实际哈希。
func SameSHA(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Propose 暂存文件建议并核对原指纹，不直接修改项目；同路径的新建议替换旧建议。
func (w *Workspace) Propose(relative, content string, expected *string) (shared.Object, error) {
	if len(content) > maxFileBytes || !utf8.ValidString(content) {
		return nil, errors.New("建议必须是最多 200 KB 的 UTF-8 文本。")
	}
	rel, err := w.Resolve(relative, true)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, errors.New("不能修改项目根目录。")
	}
	var actual *string
	current, err := w.Read(relative)
	if err == nil {
		actual = &current.SHA
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if !SameSHA(actual, expected) {
		return nil, errors.New("文件版本不匹配，请重新读取；新文件的 expectedSha256 应为 null。")
	}
	key := filepath.ToSlash(rel)
	if _, ok := w.Proposals[key]; !ok {
		if len(w.Proposals) >= 20 {
			return nil, errors.New("单个任务最多提出 20 个文件修改。")
		}
		w.order = append(w.order, key)
	}
	w.Proposals[key] = Proposal{Path: key, OriginalSHA: actual, Content: content}
	return shared.Object{"staged": true, "path": key, "applied": false}, nil
}

// ProposeEdit 使用唯一文本片段精确替换，避免为少量修改传递整份文件。
func (w *Workspace) ProposeEdit(relative string, edits []Edit, expected string) (shared.Object, error) {
	if len(edits) < 1 || len(edits) > 40 {
		return nil, errors.New("edits 必须包含 1–40 个精确替换片段。")
	}
	current, err := w.Read(relative)
	if err != nil {
		return nil, err
	}
	if current.SHA != expected {
		return nil, errors.New("文件版本不匹配，请重新读取。")
	}
	content := current.Content
	for _, edit := range edits {
		index := strings.Index(content, edit.OldText)
		if edit.OldText == "" || index < 0 || strings.Contains(content[index+1:], edit.OldText) {
			return nil, errors.New("oldText 必须精确且唯一匹配，请补充上下文。")
		}
		content = content[:index] + edit.NewText + content[index+len(edit.OldText):]
	}
	result, err := w.Propose(relative, content, &expected)
	if err == nil {
		proposal := w.Proposals[shared.Str(result["path"])]
		proposal.Edits = edits
		w.Proposals[proposal.Path] = proposal
	}
	return result, err
}

// Execute 只开放读取、搜索和暂存建议，不提供写盘、命令执行或其他 Agent 调用。
func (w *Workspace) Execute(name string, args any) (any, error) {
	if text, ok := args.(string); ok {
		if json.Unmarshal([]byte(text), &args) != nil {
			return nil, errors.New("工具参数 JSON 无效。")
		}
	}
	input, ok := args.(map[string]any)
	if !ok {
		return nil, errors.New("工具参数必须为对象。")
	}
	path, pathOK := input["path"].(string)
	if !pathOK {
		return nil, errors.New("path 必须是字符串。")
	}
	switch name {
	case "list_files":
		return w.List(path, input["recursive"] == true)
	case "read_file":
		return w.Read(path)
	case "search_text":
		return w.Search(shared.Str(input["query"]), path)
	case "propose_file":
		content, ok := input["content"].(string)
		if !ok {
			return nil, errors.New("content 必须是字符串。")
		}
		raw, exists := input["expectedSha256"]
		if !exists {
			return nil, errors.New("缺少 expectedSha256。")
		}
		var sha *string
		if raw != nil {
			text, ok := raw.(string)
			if !ok {
				return nil, errors.New("指纹类型无效。")
			}
			sha = &text
		}
		return w.Propose(path, content, sha)
	case "propose_edit":
		var edits []Edit
		if json.Unmarshal(shared.Marshal(input["edits"]), &edits) != nil {
			return nil, errors.New("替换片段无效。")
		}
		return w.ProposeEdit(path, edits, shared.Str(input["expectedSha256"]))
	}
	return nil, errors.New("未知文件工具。")
}

// Schema 声明严格对象参数，所有属性必填，禁止额外字段。
func Schema(properties shared.Object) shared.Object {
	required := []string{}
	for key := range properties {
		required = append(required, key)
	}
	return shared.Object{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

var WorkspaceTools = []shared.Tool{
	{Name: "list_files", Description: "List bounded project files, excluding dependencies, credentials and generated directories.", Schema: Schema(shared.Object{"path": shared.Object{"type": "string"}, "recursive": shared.Object{"type": "boolean"}})},
	{Name: "read_file", Description: "Read a project UTF-8 file and SHA-256 using a relative path.", Schema: Schema(shared.Object{"path": shared.Object{"type": "string"}})},
	{Name: "search_text", Description: "Search literal text case-insensitively. Results include line numbers and truncation status.", Schema: Schema(shared.Object{"path": shared.Object{"type": "string"}, "query": shared.Object{"type": "string"}})},
	{Name: "propose_edit", Description: "Prefer small edits: unique oldText/newText replacements against original SHA. Submit all edits for a file together. Never writes files.", Schema: Schema(shared.Object{"path": shared.Object{"type": "string"}, "expectedSha256": shared.Object{"type": "string"}, "edits": shared.Object{"type": "array", "minItems": 1, "maxItems": 40, "items": Schema(shared.Object{"oldText": shared.Object{"type": "string"}, "newText": shared.Object{"type": "string"}})}})},
	{Name: "propose_file", Description: "Stage complete file content for parent review. Use original SHA, or null for a new file. Never writes files.", Schema: Schema(shared.Object{"path": shared.Object{"type": "string"}, "content": shared.Object{"type": "string"}, "expectedSha256": shared.Object{"type": []string{"string", "null"}}})},
}

type ApplyError struct {
	Cause   error
	Applied []string
}

// Error 保留部分成功的路径信息，由 CLI 另行输出 applied 数组。
func (e *ApplyError) Error() string { return e.Cause.Error() }

// ApplyProposals 由主 Agent 审查后执行；先核对所有文件，再逐个原子替换并报告部分成功。
func ApplyProposals(taskID, workspace string, paths []string, storage string) (shared.Object, error) {
	if !shared.IDPattern.MatchString(taskID) {
		return nil, errors.New("无效的 task_id。")
	}
	data, err := os.ReadFile(filepath.Join(storage, taskID+".json"))
	if err != nil {
		return nil, errors.New("无法读取任务记录。")
	}
	var task struct {
		Status    string     `json:"status"`
		Workspace string     `json:"workspace"`
		Changes   []Proposal `json:"changes"`
	}
	if json.Unmarshal(data, &task) != nil {
		return nil, errors.New("任务记录格式无效。")
	}
	w, err := OpenWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if task.Status != "completed" || !shared.SamePath(w.Root, task.Workspace) {
		return nil, errors.New("只能应用已完成且属于指定项目的任务。")
	}
	selected := []Proposal{}
	for _, p := range task.Changes {
		if len(paths) == 0 || shared.Contains(paths, p.Path) {
			selected = append(selected, p)
		}
	}
	for _, path := range paths {
		found := false
		for _, p := range selected {
			if p.Path == path {
				found = true
			}
		}
		if !found {
			return nil, errors.New("选中文件不在任务建议中。")
		}
	}
	seen := map[string]bool{}
	for _, p := range selected {
		if p.Redacted {
			return nil, errors.New("含脱敏占位符的建议不能直接应用。")
		}
		result, err := w.Propose(p.Path, p.Content, p.OriginalSHA)
		if err != nil {
			return nil, err
		}
		key := shared.Str(result["path"])
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return nil, errors.New("任务记录含重复文件建议。")
		}
		seen[key] = true
		if err = w.rejectLink(p.Path); err != nil {
			return nil, err
		}
	}
	applied := []string{}
	for _, p := range selected {
		if err = w.applyOne(p); err != nil {
			return nil, &ApplyError{err, applied}
		}
		applied = append(applied, p.Path)
	}
	return shared.Object{"task_id": taskID, "applied": applied, "changesApplied": true}, nil
}

// rejectLink 禁止替换符号链接本身，要求建议针对真实普通文件。
func (w *Workspace) rejectLink(path string) error {
	rel, err := w.Resolve(path, true)
	if err != nil {
		return err
	}
	info, err := w.handle.Lstat(rel)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("不能直接替换符号链接。")
	}
	return nil
}

// applyOne 使用受限目录句柄写入临时文件，提交前再次校验 SHA；新文件使用独占链接。
func (w *Workspace) applyOne(p Proposal) error {
	rel, err := w.Resolve(p.Path, true)
	if err != nil {
		return err
	}
	if err = w.handle.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return err
	}
	if _, err = w.Propose(p.Path, p.Content, p.OriginalSHA); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if p.OriginalSHA != nil {
		stat, err := w.handle.Stat(rel)
		if err != nil {
			return err
		}
		mode = stat.Mode().Perm()
	}
	temp := rel + ".proposal-" + shared.UUID() + ".tmp"
	defer w.handle.Remove(temp)
	f, err := w.handle.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.WriteString(p.Content)
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
	if _, err = w.Propose(p.Path, p.Content, p.OriginalSHA); err != nil {
		return err
	}
	if err = w.rejectLink(p.Path); err != nil {
		return err
	}
	if p.OriginalSHA == nil {
		return w.handle.Link(temp, rel)
	}
	return w.handle.Rename(temp, rel)
}
