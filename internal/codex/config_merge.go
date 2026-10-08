package codex

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// sameConfig 比较 TOML 的实际值，忽略换行、引号、注释和字段顺序。
func sameConfig(a, b []byte) bool {
	var left, right shared.Object
	if toml.Unmarshal(a, &left) != nil || toml.Unmarshal(b, &right) != nil {
		return false
	}
	for _, parsed := range []shared.Object{left, right} {
		if value, exists := parsed["model_providers"]; exists && len(shared.Obj(value)) == 0 {
			delete(parsed, "model_providers")
		}
	}
	return reflect.DeepEqual(left, right)
}

// configConflict 仅列出字段名，不将地址、令牌或 TOML 原文送入界面和日志。
func configConflict(path string) error {
	return fmt.Errorf("Codex 配置冲突：%s 与本次托管备份不一致，无法安全自动恢复。当前配置和备份均已保留，请核对并还原该字段后重试。", path)
}

// hasManagedRoute 检查实际模型别名和本地鉴权字段，避免将普通注释或字符串误认成尚在运行的托管配置。
func hasManagedRoute(parsed shared.Object) bool {
	model := shared.Str(parsed["model"])
	if model == relayModel || strings.HasPrefix(model, relayModel+"/") {
		return true
	}
	for _, value := range shared.Obj(parsed["model_providers"]) {
		for key := range shared.Obj(shared.Obj(value)["http_headers"]) {
			if strings.EqualFold(key, "X-Api-Subagents-Token") {
				return true
			}
		}
	}
	return false
}

// providerSpans 定位指定提供商的独立表和子表，不靠注释确定边界；不明确的内联布局拒绝猜测。
func providerSpans(data []byte, id string) ([][2]int, error) {
	var parsed shared.Object
	if toml.Unmarshal(data, &parsed) != nil {
		return nil, errors.New("Codex config.toml 格式无效，未进行修改。")
	}
	parser := unstable.Parser{}
	parser.Reset(data)
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
			return nil, errors.New("无法安全定位 Codex 提供商表，配置未修改。")
		}
		for start > 0 && data[start-1] != '\n' {
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
		spans = append(spans, [2]int{begin, len(data)})
	}
	if parser.Error() != nil || (len(spans) == 0 && shared.Obj(parsed["model_providers"])[id] != nil) {
		return nil, fmt.Errorf("无法安全定位 model_providers.%s；请使用独立 TOML 表，配置和备份已保留。", id)
	}
	return spans, nil
}

// takeConfigSpans 按已验证的升序区间摘取字段，保留其余字节；不在当前配置中展开历史注释。
func takeConfigSpans(data []byte, spans [][2]int) (rest, taken []byte) {
	end := 0
	for _, span := range spans {
		rest = append(rest, data[end:span[0]]...)
		taken = append(taken, data[span[0]:span[1]]...)
		if len(taken) > 0 && taken[len(taken)-1] != '\n' {
			taken = append(taken, '\n')
		}
		end = span[1]
	}
	rest = append(rest, data[end:]...)
	return rest, taken
}

// stripConfigMarkers 只清除语法树中的独立托管注释；多行字符串中相同文字属于用户数据，必须保留。
func stripConfigMarkers(data []byte, markers []string, preserved bool) []byte {
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(data)
	spans := [][2]int{}
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Comment {
			continue
		}
		text := strings.TrimSpace(string(parser.Raw(node.Raw)))
		remove := preserved && strings.HasPrefix(text, preservedPrefix)
		for _, marker := range markers {
			remove = remove || text == marker
		}
		if !remove {
			continue
		}
		start, end := int(node.Raw.Offset), int(node.Raw.Offset+node.Raw.Length)
		lineStart := start
		for lineStart > 0 && data[lineStart-1] != '\n' {
			lineStart--
		}
		if len(bytes.TrimSpace(data[lineStart:start])) != 0 {
			continue
		}
		for end < len(data) && data[end] != '\n' {
			end++
		}
		if end < len(data) {
			end++
		}
		spans = append(spans, [2]int{lineStart, end})
	}
	if parser.Error() != nil {
		return data
	}
	rest, _ := takeConfigSpans(data, spans)
	return rest
}

// providerEqual 容许显示名称改动，但所有影响路由、协议、凭据和重试行为的字段仍须一致。
func providerEqual(a, b any) bool {
	left, leftOK := a.(map[string]any)
	right, rightOK := b.(map[string]any)
	if !leftOK || !rightOK {
		return reflect.DeepEqual(a, b)
	}
	for key, value := range left {
		if key != "name" && !reflect.DeepEqual(value, right[key]) {
			return false
		}
	}
	for key, value := range right {
		if key != "name" && !reflect.DeepEqual(value, left[key]) {
			return false
		}
	}
	return true
}

// restoreManagedValues 以磁盘备份为所有权依据做三方合并，标记丢失、重排或字段迁移均不影响恢复。
// 仅回滚仍等于 App 写入值的受控字段；用户切换到未接管的提供商时保留其模型选择和全部配置。
func restoreManagedValues(current []byte, backup codexBackup) ([]byte, error) {
	var now, written, original shared.Object
	if toml.Unmarshal(current, &now) != nil {
		return nil, errors.New("Codex config.toml 格式无效，当前配置和备份已保留。")
	}
	if toml.Unmarshal(backup.Written, &written) != nil || toml.Unmarshal(backup.Original, &original) != nil {
		return nil, errors.New("Codex 配置备份格式无效，未修改当前配置。")
	}
	nowProviders, oldProviders, sourceProviders := shared.Obj(now["model_providers"]), shared.Obj(written["model_providers"]), shared.Obj(original["model_providers"])
	if !reflect.DeepEqual(written["openai_base_url"], original["openai_base_url"]) &&
		!reflect.DeepEqual(now["openai_base_url"], written["openai_base_url"]) &&
		!reflect.DeepEqual(now["openai_base_url"], original["openai_base_url"]) {
		return nil, configConflict("openai_base_url")
	}
	owned := []string{}
	for id, value := range oldProviders {
		if !reflect.DeepEqual(value, sourceProviders[id]) {
			owned = append(owned, id)
		}
	}
	sort.Strings(owned)
	selected := shared.Str(now["model_provider"])
	managedSelection := selected == shared.Str(written["model_provider"])
	for _, id := range owned {
		managedSelection = managedSelection || selected == id
	}
	if !managedSelection && selected != "" && selected != "openai" && nowProviders[selected] == nil {
		return nil, errors.New("Codex 当前 model_provider 指向不存在的提供商；配置和备份已保留。")
	}
	restoreKeys := map[string]bool{}
	for _, key := range []string{"model", "model_provider", "model_catalog_json", "model_context_window", "model_auto_compact_token_limit", "openai_base_url"} {
		if (key == "model" || key == "model_provider") && !managedSelection {
			continue
		}
		if reflect.DeepEqual(now[key], written[key]) {
			restoreKeys[key] = true
		}
	}
	if managedSelection {
		model := shared.Str(now["model"])
		if model != relayModel && !strings.HasPrefix(model, relayModel+"/") && model != shared.Str(original["model"]) {
			return nil, configConflict("model")
		}
		restoreKeys["model"], restoreKeys["model_provider"] = true, true
	} else if model := shared.Str(now["model"]); model == relayModel || strings.HasPrefix(model, relayModel+"/") {
		return nil, errors.New("Codex 已切换独立提供商，但 model 仍是挟持模型别名；请选择该提供商的模型后重试。")
	}
	spans, err := RootKeySpans(current, restoreKeys)
	if err != nil {
		return nil, err
	}
	rest, _ := takeConfigSpans(current, spans)
	spans, err = RootKeySpans(backup.Original, restoreKeys)
	if err != nil {
		return nil, err
	}
	_, roots := takeConfigSpans(backup.Original, spans)
	var providers []byte
	for _, id := range owned {
		value := nowProviders[id]
		if reflect.DeepEqual(value, sourceProviders[id]) {
			continue
		}
		if value != nil && !providerEqual(value, oldProviders[id]) {
			keys := []string{}
			for key := range shared.Obj(value) {
				keys = append(keys, key)
			}
			for key := range shared.Obj(oldProviders[id]) {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if key != "name" && !reflect.DeepEqual(shared.Obj(value)[key], shared.Obj(oldProviders[id])[key]) {
					return nil, configConflict("model_providers." + id + "." + key)
				}
			}
			return nil, configConflict("model_providers." + id)
		}
		spans, err = providerSpans(rest, id)
		if err != nil {
			return nil, err
		}
		rest, _ = takeConfigSpans(rest, spans)
		spans, err = providerSpans(backup.Original, id)
		if err != nil {
			return nil, err
		}
		_, table := takeConfigSpans(backup.Original, spans)
		providers = append(providers, table...)
	}
	result := append(roots, rest...)
	if len(providers) > 0 {
		result = append(result, '\n')
		result = append(result, providers...)
	}
	result = stripConfigMarkers(result, []string{defaultsBegin, defaultsEnd, providerBegin, providerEnd, inactiveBegin, inactiveEnd}, true)
	var check shared.Object
	if toml.Unmarshal(result, &check) != nil {
		return nil, errors.New("恢复后配置校验失败，当前配置和备份已保留。")
	}
	// 用完整语义结果再次校验局部文本编辑，防止特殊表布局改变未接管字段的归属。
	for key := range restoreKeys {
		if value, exists := original[key]; exists {
			now[key] = value
		} else {
			delete(now, key)
		}
	}
	for _, id := range owned {
		if value, exists := sourceProviders[id]; exists {
			nowProviders[id] = value
		} else {
			delete(nowProviders, id)
		}
	}
	if len(nowProviders) == 0 {
		delete(now, "model_providers")
	} else {
		now["model_providers"] = nowProviders
	}
	if providers, exists := check["model_providers"]; exists && len(shared.Obj(providers)) == 0 {
		delete(check, "model_providers")
	}
	if !reflect.DeepEqual(check, now) {
		return nil, errors.New("恢复后存在非托管字段变化，当前配置和备份已保留。")
	}
	return result, nil
}
