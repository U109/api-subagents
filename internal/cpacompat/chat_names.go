// 从 CLIProxyAPI v7.3.6 移植，MIT 许可；来源与本地适配范围见 README.md 和 LICENSE
package cpacompat

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const responsesChatToolNameLimit = 64

// responsesToolDeclaration 保存原始身份和出站别名，供声明、历史、指定工具共用
type responsesToolDeclaration struct {
	chatName, localName, namespace string
}

// responsesToolName 读取标准名称或兼容的 function.name，忽略名称首尾空白
func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

// walkResponsesToolDeclarations 按声明顺序收集工具、命名空间和追加工具，统一计算上游别名，避免往返时身份不一致
func walkResponsesToolDeclarations(root gjson.Result, visit func(responsesToolDeclaration) bool) {
	var declarations []responsesToolDeclaration
	emit := func(tool gjson.Result, namespaceName string) {
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "", "function", "custom":
		default:
			return
		}
		localName := responsesToolName(tool)
		if localName == "" {
			return
		}
		declarations = append(declarations, responsesToolDeclaration{
			chatName:  qualifyResponsesNamespaceToolName(namespaceName, localName),
			localName: localName,
			namespace: namespaceName,
		})
	}
	scan := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
				if children := tool.Get("tools"); children.Exists() && children.IsArray() {
					namespaceName := strings.TrimSpace(tool.Get("name").String())
					children.ForEach(func(_, child gjson.Result) bool {
						emit(child, namespaceName)
						return true
					})
				}
				return true
			}
			emit(tool, "")
			return true
		})
	}

	scan(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				scan(item.Get("tools"))
			}
			return true
		})
	}

	disambiguateResponsesChatToolNames(declarations)

	proceed := true
	for _, declaration := range declarations {
		if !proceed {
			break
		}
		proceed = visit(declaration)
	}
}

// disambiguateResponsesChatToolNames 先保留短名称和不歧义的局部名称，再为截短后冲突的长名称分配独立后缀
func disambiguateResponsesChatToolNames(declarations []responsesToolDeclaration) {
	claimed := make(map[string]string, len(declarations))
	claim := func(candidate, identity string) bool {
		if ownerClaim, taken := claimed[candidate]; !taken {
			claimed[candidate] = identity
			return true
		} else {
			return ownerClaim == identity
		}
	}
	longDeclarations := make([]int, 0)
	identities := make([]string, len(declarations))
	// localName → the single identity that declares it, or "" once a second,
	// distinct identity shows the name is ambiguous.
	localOwners := make(map[string]string)
	ambiguousLocalNames := make(map[string]struct{})
	for i := range declarations {
		identity := rawResponsesNamespaceQualifiedName(declarations[i].namespace, declarations[i].localName)
		identities[i] = identity
		if len(identity) > responsesChatToolNameLimit {
			longDeclarations = append(longDeclarations, i)
		} else {
			claim(identity, identity)
		}
		local := declarations[i].localName
		if local == "" || local == identity || len(local) > responsesChatToolNameLimit {
			continue
		}
		if ownerLocal, seen := localOwners[local]; !seen {
			localOwners[local] = identity
		} else if ownerLocal != "" && ownerLocal != identity {
			localOwners[local] = ""
		}
	}
	for local, ownerLocal := range localOwners {
		// Reserving under any identity keeps the name out of every later
		// truncation alias; ambiguous names additionally never get emitted.
		claim(local, ownerLocal)
		if ownerLocal == "" {
			ambiguousLocalNames[local] = struct{}{}
		}
	}
	isAmbiguous := func(name string) bool {
		_, ambiguous := ambiguousLocalNames[name]
		return ambiguous
	}
	for _, i := range longDeclarations {
		identity := identities[i]
		name := declarations[i].chatName
		if !isAmbiguous(name) && claim(name, identity) {
			continue
		}
		for suffix := 1; ; suffix++ {
			candidate := capResponsesChatToolName(name + "_" + strconv.Itoa(suffix))
			if isAmbiguous(candidate) {
				continue
			}
			if claim(candidate, identity) {
				declarations[i].chatName = candidate
				break
			}
		}
	}
}

// qualifyResponsesNamespaceToolName 组合命名空间后应用兼容接口的 64 字符上限
func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	return capResponsesChatToolName(rawResponsesNamespaceQualifiedName(namespaceName, childName))
}

// rawResponsesNamespaceQualifiedName 按 CPA 规则生成完整工具身份，保留 MCP 已限定名称和末尾分隔符
func rawResponsesNamespaceQualifiedName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName) {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

// capResponsesChatToolName 保留长工具名的末尾并去除起始分隔符，使名称满足严格上游的长度约束
func capResponsesChatToolName(name string) string {
	if len(name) <= responsesChatToolNameLimit {
		return name
	}
	truncated := name[len(name)-responsesChatToolNameLimit:]
	if trimmed := strings.TrimLeft(truncated, "_-"); trimmed != "" {
		return trimmed
	}
	return truncated
}

// resolveResponsesQualifiedToolIdentity 根据声明反查上游别名对应的原始局部名称和命名空间，采用首个有效声明
func resolveResponsesQualifiedToolIdentity(root gjson.Result, qualifiedName string) (name, namespace string, found bool) {
	walkResponsesToolDeclarations(root, func(declaration responsesToolDeclaration) bool {
		if declaration.chatName != qualifiedName {
			return true
		}
		name, namespace, found = declaration.localName, declaration.namespace, true
		return false
	})
	return name, namespace, found
}

// chatNameForResponsesNamespaceToolCall 让带命名空间的历史调用和指定工具使用与声明相同的别名，未知身份不得占用已声明别名
func chatNameForResponsesNamespaceToolCall(requestRawJSON []byte, namespace, localName string) string {
	root := gjson.ParseBytes(requestRawJSON)
	qualified := ""
	walkResponsesToolDeclarations(root, func(declaration responsesToolDeclaration) bool {
		if declaration.namespace == namespace && declaration.localName == localName {
			qualified = declaration.chatName
			return false
		}
		return true
	})
	if qualified != "" {
		return qualified
	}
	// An identity no current declaration backs (history from an older build,
	// or a foreign client) still needs a chat-legal name, but not one that a
	// real declaration owns — that would attribute the call to that tool.
	return avoidResponsesDeclaredChatAliases(root, qualifyResponsesNamespaceToolName(namespace, localName))
}

// canonicalResponsesToolName 解析省略命名空间的调用；优先精确身份，仅在局部名称唯一时补全，避免错误路由
func canonicalResponsesToolName(requestRawJSON []byte, name string) string {
	root := gjson.ParseBytes(requestRawJSON)
	if _, _, found := resolveResponsesQualifiedToolIdentity(root, name); found {
		return name
	}
	// A replayed call may carry the fully-qualified uncapped name of a long
	// declaration (history recorded by an older build, or a foreign client
	// that flattened the qualified name itself). Resolve it to that
	// declaration's emitted chat name before the bare local-name lookup, which
	// could otherwise hand the call to a different declaration that happens to
	// use the whole qualified name as its own child name, and before the blind
	// cap, which could collide with a declaration whose original name equals
	// the long declaration's capped tail.
	chatName := ""
	walkResponsesToolDeclarations(root, func(declaration responsesToolDeclaration) bool {
		if rawResponsesNamespaceQualifiedName(declaration.namespace, declaration.localName) == name {
			chatName = declaration.chatName
			return false
		}
		return true
	})
	if chatName != "" {
		return chatName
	}
	seen := make(map[string]struct{})
	candidate := ""
	ambiguous := false
	walkResponsesToolDeclarations(root, func(declaration responsesToolDeclaration) bool {
		if _, duplicate := seen[declaration.chatName]; duplicate {
			return true
		}
		seen[declaration.chatName] = struct{}{}
		if declaration.localName == name {
			if candidate != "" {
				ambiguous = true
			}
			candidate = declaration.chatName
		}
		return true
	})
	if candidate != "" && !ambiguous {
		return candidate
	}
	// A name that no current declaration produced (unresolved or ambiguous
	// local-name matches above, or history from an older build): still enforce
	// the chat tool name limit, but never land on a declared alias — that
	// would attribute the call to whichever declaration happens to own the
	// capped value.
	return avoidResponsesDeclaredChatAliases(root, capResponsesChatToolName(name))
}

// avoidResponsesDeclaredChatAliases 为未知或歧义历史名称避开已声明工具，防止误调用另一工具
func avoidResponsesDeclaredChatAliases(root gjson.Result, candidate string) string {
	claimed := make(map[string]struct{})
	walkResponsesToolDeclarations(root, func(declaration responsesToolDeclaration) bool {
		claimed[declaration.chatName] = struct{}{}
		return true
	})
	if _, taken := claimed[candidate]; !taken {
		return candidate
	}
	for suffix := 1; ; suffix++ {
		variant := capResponsesChatToolName(candidate + "_" + strconv.Itoa(suffix))
		if _, taken := claimed[variant]; !taken {
			return variant
		}
	}
}
