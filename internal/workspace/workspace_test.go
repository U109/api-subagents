package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestWorkspaceBoundaries 保证读取不越界，凭据及生成目录被拒绝，暂存建议不改磁盘。
func TestWorkspaceBoundaries(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello 世界"), 0644)
	os.WriteFile(filepath.Join(root, "models.json"), []byte("private"), 0600)
	w, err := OpenWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, path := range []string{"../outside", "/absolute", "C:/absolute", "a\\b", ".env", "models.json", "sub/../models.json", "node_modules/x", ".tools/x", "file.key"} {
		t.Run(path, func(t *testing.T) {
			if _, err := w.Resolve(path, true); err == nil {
				t.Fatal("restricted path accepted")
			}
		})
	}
	file, err := w.Read("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.ProposeEdit("hello.txt", []Edit{{"世界", "朋友"}}, file.SHA); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "hello.txt"))
	if string(data) != "hello 世界" {
		t.Fatal("proposal wrote file")
	}
	if _, err = w.ProposeEdit("hello.txt", []Edit{{"hello", "hi"}}, "stale"); err == nil {
		t.Fatal("stale hash accepted")
	}
	if _, err = w.Propose("new/file.txt", "new", nil); err != nil {
		t.Fatal(err)
	}
}

// TestTextAndSearchLimits 检查二进制、非法 UTF-8、超大文件及有界搜索，不整目录加载到内存。
func TestTextAndSearchLimits(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string][]byte{"binary": {0, 1}, "invalid": {255}, "large": []byte(strings.Repeat("x", maxFileBytes+1)), "small": []byte("Target\ntarget\n")} {
		os.WriteFile(filepath.Join(root, name), data, 0644)
	}
	w, _ := OpenWorkspace(root)
	defer w.Close()
	for _, path := range []string{"binary", "invalid", "large"} {
		if _, err := w.Read(path); err == nil {
			t.Fatal("invalid file read")
		}
	}
	result, err := w.Search("TARGET", ".")
	if err != nil || len(shared.Arr(result["matches"])) != 2 {
		t.Fatal(result, err)
	}
	os.WriteFile(filepath.Join(root, "repeat"), []byte("aaa"), 0644)
	file, _ := w.Read("repeat")
	if _, err = w.ProposeEdit("repeat", []Edit{{"aa", "b"}}, file.SHA); err == nil {
		t.Fatal("overlapping ambiguous edit accepted")
	}
}

// TestWorkspaceLinks 检查项目外链接和指向凭据的别名；系统不允许创建链接时明确跳过。
func TestWorkspaceLinks(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(external, []byte("secret"), 0600)
	if err := os.Symlink(external, filepath.Join(root, "escape")); err != nil {
		t.Skip("symlink not available")
	}
	os.WriteFile(filepath.Join(root, "models.json"), []byte("secret"), 0600)
	os.Symlink("models.json", filepath.Join(root, "alias"))
	w, _ := OpenWorkspace(root)
	defer w.Close()
	for _, name := range []string{"escape", "alias"} {
		if _, err := w.Read(name); err == nil {
			t.Fatal("symlink boundary bypassed")
		}
	}
}

// TestApplyPreflight 先核对所有选中文件，再执行写入；冲突、脱敏和重复路径均拒绝。
func TestApplyPreflight(t *testing.T) {
	for _, kind := range []string{"success", "stale", "redacted", "duplicate", "wrong-workspace", "failed"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			storage := t.TempDir()
			id := shared.UUID()
			os.WriteFile(filepath.Join(root, "a.txt"), []byte("old"), 0644)
			sha := shared.Hash([]byte("old"))
			changes := []Proposal{{Path: "a.txt", OriginalSHA: &sha, Content: "new"}, {Path: "b.txt", Content: "second"}}
			status := "completed"
			workspace := root
			switch kind {
			case "stale":
				bad := "stale"
				changes[1].OriginalSHA = &bad
			case "redacted":
				changes[1].Redacted = true
			case "duplicate":
				changes = append(changes, changes[0])
			case "wrong-workspace":
				workspace = t.TempDir()
			case "failed":
				status = "failed"
			}
			shared.AtomicWrite(filepath.Join(storage, id+".json"), shared.Marshal(shared.Object{"status": status, "workspace": workspace, "changes": changes}), 0600)
			result, err := ApplyProposals(id, root, nil, storage)
			data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if kind == "success" {
				if err != nil || string(data) != "new" || result["changesApplied"] != true {
					t.Fatal(result, err)
				}
			} else if err == nil || string(data) != "old" {
				t.Fatal("preflight allowed write", err)
			}
		})
	}
}
