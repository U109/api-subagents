package codex

import (
	"bytes"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

// TestPreviousProviderBridge 验证默认和历史自定义提供商都改走回环，关闭时逐字恢复并保留非托管编辑。
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
		for _, name := range []string{id, "other", providerID} {
			provider := shared.Obj(providers[name])
			if provider["base_url"] != "http://127.0.0.1:12345/v1" || provider["supports_websockets"] != false || provider["env_key"] != nil || provider["requires_openai_auth"] != false || shared.Obj(provider["http_headers"])["Authorization"] != nil {
				t.Fatal("old provider was not fully isolated", name)
			}
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

// TestPreviousProviderUnsupportedLayout 确保默认或历史提供商的内联表都不会留下双重路由或覆盖原配置。
func TestPreviousProviderUnsupportedLayout(t *testing.T) {
	for _, selected := range []string{"custom", "openai"} {
		_, err := PatchCodexConfig([]byte("model_provider='"+selected+"'\nmodel_providers.custom={name='Custom',base_url='https://example.invalid'}\n"), "C:/catalog.json", 12345, "token")
		if err == nil {
			t.Fatal("unsupported inline provider silently left upstream routing active", selected)
		}
	}
}

// TestHistoricalProviderWithOpenAIDefault 验证内置 OpenAI 和历史自定义身份都被临时接管，退出后恢复原设置。
func TestHistoricalProviderWithOpenAIDefault(t *testing.T) {
	original := []byte("model='old'\nmodel_provider='openai'\nopenai_base_url='https://official.invalid/v1'\n[model_providers.custom]\nname='Historical'\nbase_url='https://old.invalid/v1'\nrequires_openai_auth=true\nsupports_websockets=true\n")
	written, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "synthetic-local-token")
	if err != nil {
		t.Fatal(err)
	}
	var parsed shared.Object
	if err := toml.Unmarshal(written, &parsed); err != nil {
		t.Fatal(err)
	}
	providers := shared.Obj(parsed["model_providers"])
	for _, id := range []string{"custom"} {
		if shared.Obj(providers[id])["base_url"] != "http://127.0.0.1:12345/v1" {
			t.Fatal("historical provider bypassed the relay", id)
		}
	}
	if providers["openai"] != nil || parsed["openai_base_url"] != "http://127.0.0.1:12345/v1/builtin/synthetic-local-token" {
		t.Fatal("built-in provider was not safely redirected")
	}
	again, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "synthetic-local-token")
	if err != nil || !bytes.Equal(written, again) {
		t.Fatal("provider order is not deterministic", err)
	}
	current := bytes.Replace(written, []byte(`model_provider = "api_subagents"`), []byte(`model_provider = "custom"`), 1)
	current = bytes.Replace(current, []byte(`model = "api-subagents"`), []byte(`model = "api-subagents/demo"`), 1)
	restored, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
	if err != nil || !sameConfig(restored, original) {
		t.Fatal("historical provider selection blocked recovery", err)
	}
}

// TestOriginalModelRouting 旧模型名统一跟随 App 当前连接和默认模型，不再借用旧模型所在连接的凭据。
func TestOriginalModelRouting(t *testing.T) {
	c := configstore.EmptyConfig()
	c.Models["one"] = configstore.Profile{Model: "shared", RelayModels: []string{"only-one"}, APIKey: "one-key"}
	c.Models["two"] = configstore.Profile{Model: "shared", APIKey: "two-key"}
	for _, tc := range []struct{ current, model, want string }{{"one", "shared", "one"}, {"two", "only-one", "two"}, {"missing", "shared", ""}, {"one", "unknown", "one"}} {
		name, profile, err := ResolveCatalogModel(c, tc.current, tc.model)
		if tc.want == "" {
			if err == nil {
				t.Fatal("unlisted or ambiguous model accepted")
			}
		} else if err != nil || name != tc.want || profile.Model != c.Models[tc.want].Model || profile.APIKey != tc.want+"-key" {
			t.Fatal("wrong route or credentials", tc, err)
		}
	}
}
