package codex

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// preservePreviousProvider 暂存原默认自定义提供商的整段配置，让绑定该身份的旧对话也能经过本地网关。
// 仅接管明确存在的用户提供商，不覆盖内置 OpenAI；复杂且无法完整定位的写法会在写盘前停止。
func preservePreviousProvider(original []byte, parsed shared.Object) ([]byte, string, error) {
	id := shared.Str(parsed["model_provider"])
	if id == "" || id == "openai" || id == providerID || shared.Obj(parsed["model_providers"])[id] == nil {
		return original, "", nil
	}
	parser := unstable.Parser{}
	parser.Reset(original)
	spans := [][2]int{}
	begin := -1
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Table && node.Kind != unstable.ArrayTable {
			continue
		}
		keys := node.Key()
		parts := []string{}
		start := -1
		for keys.Next() {
			key := keys.Node()
			if start < 0 {
				start = int(key.Raw.Offset)
			}
			parts = append(parts, string(key.Data))
		}
		if start < 0 {
			return nil, "", errors.New("无法定位原自定义提供商，未修改 Codex 配置")
		}
		for start > 0 && original[start-1] != '\n' {
			start--
		}
		if begin >= 0 {
			spans = append(spans, [2]int{begin, start})
			begin = -1
		}
		if len(parts) >= 2 && parts[0] == "model_providers" && parts[1] == id {
			begin = start
		}
	}
	if begin >= 0 {
		spans = append(spans, [2]int{begin, len(original)})
	}
	if parser.Error() != nil {
		return nil, "", errors.New("原自定义提供商解析失败，未修改 Codex 配置")
	}
	result := append([]byte(nil), original...)
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		marker := preservedPrefix + base64.StdEncoding.EncodeToString(original[span[0]:span[1]]) + "\n"
		result = bytes.Join([][]byte{result[:span[0]], []byte(marker), result[span[1]:]}, nil)
	}
	var check shared.Object
	if len(spans) == 0 || toml.Unmarshal(result, &check) != nil || shared.Obj(check["model_providers"])[id] != nil {
		return nil, "", errors.New("原自定义提供商使用了无法安全接管的 TOML 写法，请将其写在独立的 model_providers 表中")
	}
	return result, id, nil
}

// localProviderConfig 两个提供商身份使用同一本地入口；原远程凭据和 WebSocket 设置不会沿用。
func localProviderConfig(id string, port int, token string) string {
	return fmt.Sprintf("[model_providers.%s]\nname = \"API Subagents · 挟持模式\"\nbase_url = \"http://127.0.0.1:%d/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = false\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\nstream_idle_timeout_ms = 650000\nhttp_headers = { \"X-Api-Subagents-Token\" = %q }\n", id, port, token)
}
