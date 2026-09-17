# CPA 兼容代码移植

本目录直接移植并适配 [CLIProxyAPI v7.3.6](https://github.com/router-for-me/CLIProxyAPI/tree/v7.3.6) 的相关代码，固定来源提交 `8c664b2fede5c83b919be1df9b01057ec4e4c950`。版权及 MIT 许可保存在 [LICENSE](LICENSE)，发布包同时携带仓库根目录的第三方声明。

| 本地文件 | CPA 来源 | 移植范围与适配 |
| --- | --- | --- |
| chat_names.go | internal/translator/openai/openai/responses/openai_openai-responses_tools.go | 工具名称读取、命名空间、64 字符限制、冲突消解、历史与指定工具映射；按需摘取函数并补中文说明 |
| chat_tools.go | 本项目接入代码 | 将上述算法接到现有转换器前后；仅对出现超长工具名的请求启用，别名只存在于单次请求 |
| gemini_schema.go | internal/util/gemini_schema.go 的 removeUnsupportedKeywords | 移植新增 schema 标识关键字清理规则；适配为按 schema 结构递归，避免误改业务参数与默认数据 |
| claude_effort.go | internal/thinking/convert.go 的 MapToClaudeEffort | 直接移植档位映射；由调用处决定模型是否允许 max |
| kimi_effort.go | internal/thinking/provider/kimi/apply.go 的 applyEnabledThinking / applyDisabledThinking | 直接移植 Kimi Coding 开关、档位和旧字段清理；不套用于 Moonshot 开放 API |

基础 Responses、Chat Completions、Claude、Gemini 流转换继续使用已锁定的 CPA SDK v7.3.4；本次不升级 SDK，也不复制服务器、账号管理、OAuth、重试调度或未使用的转换器。Claude Opus 4.6 的 xhigh 按映射发送 max；其他现有自适应 Claude 模型保留 high 上限。

CPA 未提供 DeepSeek、豆包、MiniMax、GLM 的独立适配器。本项目在 `internal/providers/compatibility.go` 中按官方协议补充这些模型及 Moonshot 的参数处理；`chat_metadata.go` 处理结构化思考。`internal/relay/chat_history.go` 是本项目的有限内存回放实现，不复制 CPA 的账户级缓存、磁盘或 Redis 依赖。各策略及边界见[模型兼容说明](../../docs/model-compatibility.md)。

官方主机自动选择策略；自定义网关默认通用，也可按模型明确指定厂商。通过 CPA 接入时保留通用模式，避免重复转换。移植代码的完整许可保存在本目录，构建工具会将它加入根目录第三方声明。

验证分为本包边界测试与 `internal/relay/cpa_compat_test.go` 的本地 HTTP 往返测试。后续同步 CPA 时应比较以上固定来源，复测工具名称冲突、历史回放、自由文本工具、流式响应和 schema 数据边界。模拟通过不代表所有付费服务和模型都已实测。
