package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

// TestCatalogContextWindows 验证同一连接内不同模型与跟随别名分别声明容量和压缩阈值。
func TestCatalogContextWindows(t *testing.T) {
	c := configstore.EmptyConfig()
	c.Models["demo"] = configstore.Profile{Model: "one", RelayModels: []string{"two", "unknown"}, ModelContextWindows: map[string]int{"one": 128000, "two": 1000000}}
	disk := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	if err := disk.WriteCatalog(c, "demo"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(disk.catalogPath())
	if err != nil {
		t.Fatal(err)
	}
	var catalog shared.Object
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{relayModel: 128000, RelayModelAlias("demo", "one"): 128000, RelayModelAlias("demo", "two"): 1000000, RelayModelAlias("demo", "unknown"): configstore.DefaultContextWindow}
	if len(shared.Arr(catalog["models"])) != len(want) {
		t.Fatal("missing catalog entries")
	}
	for _, value := range shared.Arr(catalog["models"]) {
		model := shared.Obj(value)
		context := want[shared.Str(model["slug"])]
		if shared.Int(model["context_window"]) != context || shared.Int(model["auto_compact_token_limit"]) != context*9/10 {
			t.Fatal("catalog capacity mismatch", model["slug"])
		}
	}
}

// TestContextOverridesRestore 确保原全局容量不会压过模型目录，停用时连同外部修改一并安全还原。
func TestContextOverridesRestore(t *testing.T) {
	original := []byte("model = 'old'\nmodel_context_window = 8192 # custom\nmodel_auto_compact_token_limit = 6000\n[features]\nfoo = false\n")
	written, err := PatchCodexConfig(original, "C:/models.json", 12345, "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	var parsed shared.Object
	if toml.Unmarshal(written, &parsed) != nil || parsed["model_context_window"] != nil || parsed["model_auto_compact_token_limit"] != nil {
		t.Fatal("global context still overrides catalog")
	}
	modified := bytes.Replace(written, []byte("foo = false"), []byte("foo = true"), 1)
	restored, err := RestoreCodexConfig(modified, codexBackup{Original: original, Written: written})
	want := bytes.Replace(original, []byte("foo = false"), []byte("foo = true"), 1)
	if err != nil || !bytes.Equal(bytes.TrimSpace(restored), bytes.TrimSpace(want)) {
		t.Fatal("context restore lost values/comments", err)
	}
}
