package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	toml "github.com/pelletier/go-toml/v2"
)

// assertGuardianContextRemoved 检查主配置和所有 profile，不把其他表中同名的用户字段误判为废弃设置。
func assertGuardianContextRemoved(t *testing.T, data []byte) {
	t.Helper()
	var parsed shared.Object
	if err := toml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	scopes := []shared.Object{parsed}
	for _, value := range shared.Obj(parsed["profiles"]) {
		scopes = append(scopes, shared.Obj(value))
	}
	for _, scope := range scopes {
		guardian := shared.Obj(shared.Obj(scope["features"])["guardianv2"])
		if _, exists := guardian["thread_context"]; exists {
			t.Fatal("deprecated Guardian context remains")
		}
	}
}

// TestPatchCodexRemovesDeprecatedGuardianContext 覆盖独立表、点号键、引号、内联表和未选中的 profile，其他字节应原样恢复。
func TestPatchCodexRemovesDeprecatedGuardianContext(t *testing.T) {
	for _, tt := range []struct {
		name, original, clean string
	}{
		{"table", "# keep\n[features.guardianv2]\nthread_context = true # obsolete\nother = false\n", "# keep\n[features.guardianv2]\nother = false\n"},
		{"profile", "profile = 'active'\n[profiles.active.features.guardianv2]\nthread_context = false\nother = true\n[profiles.'not.selected'.features.guardianv2]\n\"thread_context\" = true\n", "profile = 'active'\n[profiles.active.features.guardianv2]\nother = true\n[profiles.'not.selected'.features.guardianv2]\n"},
		{"root-dotted", "features.guardianv2.thread_context = true\nmodel = 'keep'\n", "model = 'keep'\n"},
		{"features-dotted", "[features]\n'guardianv2'.\"thread_context\" = false\napps = true\n", "[features]\napps = true\n"},
		{"profile-dotted", "[profiles.'work.dev']\nfeatures.guardianv2.thread_context = true\nmodel = 'keep'\n", "[profiles.'work.dev']\nmodel = 'keep'\n"},
		{"crlf-eof", "[features.guardianv2]\r\nother = true\r\n  thread_context = false", "[features.guardianv2]\r\nother = true\r\n"},
		{"inline-first", "features = { guardianv2 = { thread_context = true, other = false }, apps = true } # keep\n", "features = { guardianv2 = { other = false }, apps = true } # keep\n"},
		{"inline-middle", "[features]\nguardianv2 = { first = true, thread_context = false, last = true }\n", "[features]\nguardianv2 = { first = true, last = true }\n"},
		{"inline-last", "features = { guardianv2 = { other = true, thread_context = false } }\n", "features = { guardianv2 = { other = true } }\n"},
		{"inline-only", "profiles = { demo = { features = { guardianv2 = { thread_context = true } } } }\n", "profiles = { demo = { features = { guardianv2 = {  } } } }\n"},
		{"inline-dotted-siblings", "profiles = { a.features.guardianv2.thread_context = true, b.features.guardianv2.thread_context = false, c.model = 'keep' }\n", "profiles = { c.model = 'keep' }\n"},
		{"inline-dotted-tail", "profiles = { c.model = 'keep', a.features.guardianv2.thread_context = true, b.features.guardianv2.thread_context = false }\n", "profiles = { c.model = 'keep' }\n"},
		{"inline-trailing-comma", "[features]\nguardianv2 = { thread_context = true, }\n", "[features]\nguardianv2 = {  }\n"},
		{"inline-nonadjacent", "profiles = { a.features.guardianv2.thread_context = true, keep.model = 'keep', b.features.guardianv2.thread_context = false }\n", "profiles = { keep.model = 'keep' }\n"},
		{"inline-comment-comma", "features = { guardianv2 = {\nother = true # keep, comma\n, thread_context = false\n} }\n", "features = { guardianv2 = {\nother = true # keep, comma\n\n} }\n"},
		{"string-and-other-fields", "instructions = '''\n[features.guardianv2]\nthread_context = true\n'''\n# thread_context = true\n[features.guardianv2]\nthread_context = false\n[other]\nthread_context = true\n['features.guardianv2']\nthread_context = true\n", "instructions = '''\n[features.guardianv2]\nthread_context = true\n'''\n# thread_context = true\n[features.guardianv2]\n[other]\nthread_context = true\n['features.guardianv2']\nthread_context = true\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := []byte(tt.original)
			written, err := PatchCodexConfig(original, "C:/synthetic/catalog.json", 12345, "synthetic-token", relayModel)
			if err != nil {
				t.Fatal(err)
			}
			assertGuardianContextRemoved(t, written)
			restored, err := RestoreCodexConfig(written, codexBackup{Original: original, Written: written})
			if err != nil || string(restored) != tt.clean {
				t.Fatalf("migration changed unrelated content: %q, %v", restored, err)
			}
		})
	}
}

// TestGuardianContextCleanupIsSafe 验证非法 TOML 和托管字段冲突不写磁盘，缺少废弃字段时不重排原配置。
func TestGuardianContextCleanupIsSafe(t *testing.T) {
	for _, original := range [][]byte{
		[]byte("# keep\n[features.guardianv2]\nother = true\n[profiles.empty.features.guardianv2]\n"),
		[]byte("[other]\nthread_context = true\n"),
		nil,
	} {
		clean, err := withoutDeprecatedGuardianContext(original)
		if err != nil || !bytes.Equal(clean, original) {
			t.Fatal("clean config was changed", err)
		}
	}
	for _, hasBackup := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid-toml", true: "managed-conflict"}[hasBackup], func(t *testing.T) {
			c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
			t.Setenv("CODEX_HOME", c.Home)
			original := []byte("model='old'\n[features.guardianv2]\nthread_context = true\n")
			current := []byte("[features.guardianv2]\nthread_context = true\nthread_context = false\n")
			var saved []byte
			if hasBackup {
				written, err := PatchCodexConfig(original, "C:/synthetic/catalog.json", 12345, "synthetic-token", relayModel)
				if err != nil {
					t.Fatal(err)
				}
				current = bytes.Replace(written, []byte(`model = "api-subagents"`), []byte(`model = "unknown-model"`), 1)
				current = append(current, []byte("\n[profiles.demo.features.guardianv2]\nthread_context = false\n")...)
				saved = shared.Marshal(codexBackup{Original: original, Written: written, Existed: true})
				if err := os.WriteFile(c.backupPath(), saved, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(c.configPath(), current, 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.Restore(); err == nil {
				t.Fatal("unsafe config was accepted")
			}
			unchanged, err := os.ReadFile(c.configPath())
			if err != nil || !bytes.Equal(current, unchanged) {
				t.Fatal("failed restore changed config", err)
			}
			if hasBackup {
				backup, err := os.ReadFile(c.backupPath())
				if err != nil || !bytes.Equal(saved, backup) {
					t.Fatal("failed restore changed backup", err)
				}
			}
		})
	}
}

// TestRestoreLegacyGuardianContext 验证旧版备份的快速恢复、用户已删字段和外部编辑都不会重新引入废弃配置。
func TestRestoreLegacyGuardianContext(t *testing.T) {
	clean := []byte("model='old'\n[features.guardianv2]\nother = false\n[profiles.demo.features.guardianv2]\nother = true\n")
	legacy := bytes.ReplaceAll(clean, []byte("[features.guardianv2]\n"), []byte("[features.guardianv2]\nthread_context = true\n"))
	legacy = bytes.ReplaceAll(legacy, []byte("[profiles.demo.features.guardianv2]\n"), []byte("[profiles.demo.features.guardianv2]\nthread_context = false\n"))
	written, err := PatchCodexConfig(clean, "C:/synthetic/catalog.json", 12345, "synthetic-token", relayModel)
	if err != nil {
		t.Fatal(err)
	}
	legacyWritten := bytes.ReplaceAll(written, []byte("[features.guardianv2]\n"), []byte("[features.guardianv2]\nthread_context = true\n"))
	legacyWritten = bytes.ReplaceAll(legacyWritten, []byte("[profiles.demo.features.guardianv2]\n"), []byte("[profiles.demo.features.guardianv2]\nthread_context = false\n"))
	backup := codexBackup{Original: legacy, Written: legacyWritten}
	for _, tt := range []struct {
		name    string
		current []byte
		want    []byte
	}{
		{"unchanged-legacy", legacyWritten, clean},
		{"already-removed", written, clean},
		{"external-edit", bytes.Replace(legacyWritten, []byte("other = false"), []byte("other = true"), 1), bytes.Replace(clean, []byte("other = false"), []byte("other = true"), 1)},
		{"rewritten-markers", stripConfigMarkers(legacyWritten, []string{defaultsBegin, defaultsEnd, providerBegin, providerEnd}, false), clean},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restored, err := RestoreCodexConfig(tt.current, backup)
			if err != nil || !sameConfig(restored, tt.want) {
				t.Fatal("legacy restore failed", err)
			}
			assertGuardianContextRemoved(t, restored)
		})
	}
}

// TestGuardianContextDiskLifecycle 使用独立 CODEX_HOME 覆盖首次启动、开关循环及当前文件已恢复为旧原文的边界。
func TestGuardianContextDiskLifecycle(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-catalog", true: "existing-catalog"}[catalog], func(t *testing.T) {
			c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
			t.Setenv("CODEX_HOME", c.Home)
			legacy := []byte("model='old'\n[features.guardianv2]\nthread_context = true\nother = false\n[profiles.demo.features.guardianv2]\nthread_context = false\n")
			if err := os.WriteFile(c.configPath(), legacy, 0600); err != nil {
				t.Fatal(err)
			}
			if catalog {
				if err := os.WriteFile(c.catalogPath(), []byte(`{"models":[]}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Restore(); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(c.configPath())
			if err != nil {
				t.Fatal(err)
			}
			assertGuardianContextRemoved(t, first)
			if err := c.Restore(); err != nil {
				t.Fatal(err)
			}
			repeated, err := os.ReadFile(c.configPath())
			if err != nil || !bytes.Equal(first, repeated) {
				t.Fatal("repeated repair changed config", err)
			}
			// 直接开启仍需保留未经清理的原始备份，且关闭后不得恢复已弃用字段。
			if err := os.WriteFile(c.configPath(), legacy, 0600); err != nil {
				t.Fatal(err)
			}
			store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/private")
			config, _ := store.Read()
			if err := c.Enable(config, "demo", 12345, "synthetic-token"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(c.backupPath())
			if err != nil {
				t.Fatal(err)
			}
			var backup codexBackup
			if err := json.Unmarshal(data, &backup); err != nil || !bytes.Equal(backup.Original, legacy) {
				t.Fatal("original backup was not preserved", err)
			}
			active, err := os.ReadFile(c.configPath())
			if err != nil {
				t.Fatal(err)
			}
			assertGuardianContextRemoved(t, active)
			// 当前文件等于 backup.Original 时也必须清理，不能绕过迁移。
			if err := os.WriteFile(c.configPath(), legacy, 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.Restore(); err != nil {
				t.Fatal(err)
			}
			final, err := os.ReadFile(c.configPath())
			if err != nil {
				t.Fatal(err)
			}
			assertGuardianContextRemoved(t, final)
			if _, err := os.Stat(c.backupPath()); !os.IsNotExist(err) {
				t.Fatal("restore left an active backup", err)
			}
			if _, err := os.Stat(filepath.Join(c.DataRoot, "codex-recovery")); err != nil {
				t.Fatal("recovery snapshot missing", err)
			}
		})
	}
}
