package codex

import (
	"bytes"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

// TestPreviousProviderBridge 验证原提供商身份改走回环、原凭据不活跃，关闭时逐字恢复并保留非托管编辑。
func TestPreviousProviderBridge(t *testing.T) {
	for _, id := range []string{"custom", "带点.custom"} {
		original := []byte("model='old'\nmodel_provider='" + id + "'\n# before\n[model_providers.'" + id + "']\nname='Original'\nbase_url='https://example.invalid/v1'\nenv_key='OLD_TEST_KEY'\nsupports_websockets=true\n[model_providers.'" + id + "'.http_headers]\nAuthorization='Bearer synthetic-private'\n[features]\nfoo=false\n[model_providers.other]\nname='Keep'\nbase_url='https://other.invalid'\n")
		written, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "synthetic-local-token")
		if err != nil {
			t.Fatal(err)
		}
		var parsed shared.Object
		if err := toml.Unmarshal(written, &parsed); err != nil {
			t.Fatal(err)
		}
		providers := shared.Obj(parsed["model_providers"])
		for _, name := range []string{id, providerID} {
			provider := shared.Obj(providers[name])
			if provider["base_url"] != "http://127.0.0.1:12345/v1" || provider["supports_websockets"] != false || provider["env_key"] != nil || provider["requires_openai_auth"] != false || shared.Obj(provider["http_headers"])["Authorization"] != nil {
				t.Fatal("old provider was not fully isolated", name)
			}
		}
		if shared.Obj(providers["other"])["base_url"] != "https://other.invalid" {
			t.Fatal("unrelated provider changed")
		}
		for _, modified := range []bool{false, true} {
			current, want := written, original
			if modified {
				current = bytes.Replace(written, []byte("foo=false"), []byte("foo=true"), 1)
				want = bytes.Replace(original, []byte("foo=false"), []byte("foo=true"), 1)
			}
			restored, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
			if err != nil || strings.TrimSpace(string(restored)) != strings.TrimSpace(string(want)) {
				t.Fatal("provider restore failed", err)
			}
		}
		conflict := bytes.Replace(written, []byte("http://127.0.0.1:12345/v1"), []byte("http://127.0.0.1:12346/v1"), 1)
		if _, err := RestoreCodexConfig(conflict, codexBackup{Original: original, Written: written}); err == nil {
			t.Fatal("managed provider edit was overwritten")
		}
		selected := bytes.Replace(written, []byte(`model_provider = "api_subagents"`), []byte("model_provider = '"+id+"'"), 1)
		selected = bytes.Replace(selected, []byte(`model = "api-subagents"`), []byte(`model = "old"`), 1)
		if restored, err := RestoreCodexConfig(selected, codexBackup{Original: original, Written: written}); err != nil || strings.TrimSpace(string(restored)) != strings.TrimSpace(string(original)) {
			t.Fatal("old thread provider selection prevented restoration", err)
		}
	}
}

// TestPreviousProviderUnsupportedLayout 确保无法定位的内联表不会留下双重路由或覆盖原配置。
func TestPreviousProviderUnsupportedLayout(t *testing.T) {
	_, err := PatchCodexConfig([]byte("model_provider='custom'\nmodel_providers.custom={name='Custom',base_url='https://example.invalid'}\n"), "C:/catalog.json", 12345, "token")
	if err == nil {
		t.Fatal("unsupported inline provider silently left upstream routing active")
	}
}

// TestOriginalModelRouting 原模型 ID 只能使用已选模型的凭据，重复名称优先当前连接或明确拒绝。
func TestOriginalModelRouting(t *testing.T) {
	c := configstore.EmptyConfig()
	c.Models["one"] = configstore.Profile{Model: "shared", RelayModels: []string{"only-one"}, APIKey: "one-key"}
	c.Models["two"] = configstore.Profile{Model: "shared", APIKey: "two-key"}
	for _, tc := range []struct{ current, model, want string }{{"one", "shared", "one"}, {"two", "only-one", "one"}, {"missing", "shared", ""}, {"one", "unknown", ""}} {
		name, profile, err := ResolveCatalogModel(c, tc.current, tc.model)
		if tc.want == "" {
			if err == nil {
				t.Fatal("unlisted or ambiguous model accepted")
			}
		} else if err != nil || name != tc.want || profile.Model != tc.model || profile.APIKey != tc.want+"-key" {
			t.Fatal("wrong route or credentials", tc, err)
		}
	}
}
