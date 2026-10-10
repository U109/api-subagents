package codex

import (
	"errors"
	"reflect"
	"sort"

	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// guardianContextPath 只匹配主配置及单个 profile 的废弃字段，不匹配带点的单个键或其他同名设置。
func guardianContextPath(path []string) bool {
	if len(path) == 5 && path[0] == "profiles" {
		path = path[2:]
	}
	return len(path) == 3 && path[0] == "features" && path[1] == "guardianv2" && path[2] == "thread_context"
}

// configNodePath 读取 TOML 解码后的键组件，让引号和点号写法共用精确路径匹配。
func configNodePath(node *unstable.Node) []string {
	var path []string
	keys := node.Key()
	for keys.Next() {
		path = append(path, string(keys.Node().Data))
	}
	return path
}

// cleanGuardianContextValues 删除废弃字段并归一化对应空表，仅用于校验语义，不重写用户的表布局。
func cleanGuardianContextValues(parsed shared.Object) bool {
	removed := false
	// cleanScope 只处理合法的 features 表，不把其他类型的用户值当作空表删除。
	cleanScope := func(scope shared.Object) {
		features, ok := scope["features"].(map[string]any)
		if !ok {
			return
		}
		if guardian, ok := features["guardianv2"].(map[string]any); ok {
			if _, exists := guardian["thread_context"]; exists {
				delete(guardian, "thread_context")
				removed = true
			}
			if len(guardian) == 0 {
				delete(features, "guardianv2")
			}
		}
		if len(features) == 0 {
			delete(scope, "features")
		}
	}
	cleanScope(parsed)
	if profiles, ok := parsed["profiles"].(map[string]any); ok {
		for name, value := range profiles {
			if profile, ok := value.(map[string]any); ok {
				cleanScope(profile)
				if len(profile) == 0 {
					delete(profiles, name)
				}
			}
		}
		if len(profiles) == 0 {
			delete(parsed, "profiles")
		}
	}
	return removed
}

// inlineConfigComma 在已验证的内联条目间定位分隔符，跳过注释中的逗号，且不越过当前表的右括号。
func inlineConfigComma(data []byte, start, end int) int {
	for i := start; i < end; i++ {
		switch data[i] {
		case '#':
			for i < end && data[i] != '\n' {
				i++
			}
		case ',':
			return i
		case '}':
			return -1
		}
	}
	return -1
}

// inlineGuardianContextSpans 递归定位内联表中的废弃字段；相邻删除合并处理，避免留下悬空逗号。
func inlineGuardianContextSpans(data []byte, parent *unstable.Node, path []string) [][2]int {
	if parent.Value().Kind != unstable.InlineTable {
		return nil
	}
	var children []*unstable.Node
	var paths [][]string
	iterator := parent.Value().Children()
	for iterator.Next() {
		child := iterator.Node()
		children = append(children, child)
		paths = append(paths, append(append([]string{}, path...), configNodePath(child)...))
	}
	var spans [][2]int
	for i := 0; i < len(children); {
		if !guardianContextPath(paths[i]) {
			spans = append(spans, inlineGuardianContextSpans(data, children[i], paths[i])...)
			i++
			continue
		}
		j := i + 1
		for j < len(children) && guardianContextPath(paths[j]) {
			j++
		}
		start := int(children[i].Raw.Offset)
		last := children[j-1].Raw
		end := int(last.Offset + last.Length)
		switch {
		case j < len(children):
			end = int(children[j].Raw.Offset)
		case i > 0:
			previous := children[i-1].Raw
			start = inlineConfigComma(data, int(previous.Offset+previous.Length), start)
		default:
			if comma := inlineConfigComma(data, end, int(parent.Raw.Offset+parent.Raw.Length)); comma >= 0 {
				end = comma + 1
			}
		}
		spans = append(spans, [2]int{start, end})
		i = j
	}
	return spans
}

// withoutDeprecatedGuardianContext 清理主配置和全部 profile 的 thread_context，保留其他字节；解析或语义校验失败时不返回可写内容。
func withoutDeprecatedGuardianContext(data []byte) ([]byte, error) {
	var expected shared.Object
	if toml.Unmarshal(data, &expected) != nil {
		return nil, errors.New("Codex config.toml 格式无效，未清理废弃 Guardian 配置。")
	}
	if !cleanGuardianContextValues(expected) {
		return data, nil
	}
	parser := unstable.Parser{}
	parser.Reset(data)
	var table []string
	var spans [][2]int
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = configNodePath(node)
		case unstable.KeyValue:
			path := append(append([]string{}, table...), configNodePath(node)...)
			if guardianContextPath(path) {
				start, end := int(node.Raw.Offset), int(node.Raw.Offset+node.Raw.Length)
				for start > 0 && data[start-1] != '\n' {
					start--
				}
				for end < len(data) && data[end] != '\n' {
					end++
				}
				if end < len(data) {
					end++
				}
				spans = append(spans, [2]int{start, end})
			} else {
				spans = append(spans, inlineGuardianContextSpans(data, node, path)...)
			}
		}
	}
	invalid := errors.New("无法安全清理废弃 Guardian 配置，当前配置和备份已保留。")
	if parser.Error() != nil {
		return nil, invalid
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	end := 0
	for _, span := range spans {
		if span[0] < end || span[1] <= span[0] || span[1] > len(data) {
			return nil, invalid
		}
		end = span[1]
	}
	clean, _ := takeConfigSpans(data, spans)
	var actual shared.Object
	if toml.Unmarshal(clean, &actual) != nil || cleanGuardianContextValues(actual) || !reflect.DeepEqual(actual, expected) {
		return nil, invalid
	}
	return clean, nil
}
