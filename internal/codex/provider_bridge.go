package codex

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"

	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

// preservePreviousProviders 暂存自定义提供商并补齐历史身份；内置 OpenAI 由根级地址设置接管，不能创建同名表。
// 历史身份仅用于临时配置，不改聊天记录；按稳定顺序返回身份，无法定位原配置时在写盘前停止。
func preservePreviousProviders(original []byte, parsed shared.Object, historical []string) ([]byte, []string, error) {
	selected := map[string]bool{}
	selected[shared.Str(parsed["model_provider"])] = true
	for _, id := range historical {
		selected[id] = true
	}
	for id := range shared.Obj(parsed["model_providers"]) {
		selected[id] = true
	}
	delete(selected, "")
	delete(selected, providerID)
	delete(selected, "openai")
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	spans := [][2]int{}
	for _, id := range ids {
		if shared.Obj(parsed["model_providers"])[id] == nil {
			continue
		}
		provider, err := providerSpans(original, id)
		if err != nil {
			return nil, nil, err
		}
		spans = append(spans, provider...)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	result := append([]byte(nil), original...)
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		marker := preservedPrefix + base64.StdEncoding.EncodeToString(original[span[0]:span[1]]) + "\n"
		result = bytes.Join([][]byte{result[:span[0]], []byte(marker), result[span[1]:]}, nil)
	}
	var check shared.Object
	if toml.Unmarshal(result, &check) != nil {
		return nil, nil, errors.New("暂存自定义提供商后配置校验失败，未修改 Codex 配置")
	}
	for _, id := range ids {
		if shared.Obj(check["model_providers"])[id] != nil {
			return nil, nil, errors.New("原自定义提供商使用了无法安全接管的 TOML 写法，请将其写在独立的 model_providers 表中")
		}
	}
	return result, ids, nil
}

// localProviderConfig 让所有托管的提供商身份使用同一本地入口；原远程凭据和 WebSocket 设置不会沿用。
func localProviderConfig(id string, port int, token string) string {
	return fmt.Sprintf("[model_providers.%s]\nname = \"API Subagents · 挟持模式\"\nbase_url = \"http://127.0.0.1:%d/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = false\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\nstream_idle_timeout_ms = 650000\nhttp_headers = { \"X-Api-Subagents-Token\" = %q }\n", id, port, token)
}
