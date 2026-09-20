# 模型兼容

## 推荐：由上游兼容，Responses 透传

CPA 等网关支持 `/responses` 时，在「连接信息 → 接口类型」选择「Responses 透传（推荐 CPA）」。一个连接仍可勾选多个模型，在 Codex 直接切换；首次更改目录后重启 Codex。选择接口不会自动更改现有连接的地址与 Key。

本地只将 Codex 的模型别名换成所选上游模型 ID，并使用该连接凭据。其余请求内容保持原样，包括 instructions、reasoning、store、stream、tools、加密思考、历史与未知字段；不会套用 App 的流式开关或追加默认思考档位。App 的默认思考等级通过 Codex 模型目录提供，最终参数以 Codex 实际请求为准。

返回的 HTTP 状态、JSON、SSE 和上游错误正文直接转发，不清理标签、不修改用量、不合成事件、不缓存思考历史。只保留鉴权、凭据隔离、并发和超时控制；客户端取消会取消上游，失败不重试。网络中断直接结束传输，缺少 Responses 完成事件由 Codex 判断。上游必须正确实现 Responses，不能靠本地修补忽略 stream 或缺失事件的响应。

`<thinking>` 若仍出现在正文，透传会原样显示；它本身不是标准思考事件，需在 CPA／模型端正确区分正文与思考。透传不会自动隐藏这些文本。

## 旧接口：本地转换

以下兼容策略仅适用于主动选择 Chat Completions 等旧接口的连接；Responses 透传完全绕过它们。原有配置不会被自动改为其他接口，插件委派仍使用连接所选协议。

在「模型选择 → 编辑模型 → 兼容策略」设置，每个模型独立保存。该选项用于 **Chat Completions** 接口，同时作用于插件委派和挟持模式，不改写实际发送的模型 ID。

一个 worker（连接）可勾选多个厂商的模型，共用同一地址和 Key；开启挟持后，在 Codex 中直接切换具体模型，本地按本轮选择使用对应兼容策略和容量，无需创建多个 worker 或反复调整 App 默认模型。首次开启及修改模型列表后重启 Codex，已有列表中的模型切换无需重启。

默认「自动」只识别官方 API 主机名；自定义地址沿用通用 OpenAI 格式。CPA 优先使用上面的 Responses 透传；仍需 Chat Completions 时可选择「通用 Chat Completions」；自建反向代理直接转发到厂商时，可指定对应策略。仅凭模型名称无法判断网关接受哪种协议。

| 策略 | 本地处理与边界 |
| --- | --- |
| Gemini | Chat 档位映射，原生 Gemini 另有 schema 清理和思考签名处理。Gemini 3 Flash 的“关闭”使用 minimal；3 Pro 使用 low，medium 折合 high；xhigh 折合 high，不能因此完全停止思考 |
| DeepSeek | `thinking.type` 控制开关；minimal/low → low，medium/high/xhigh → high，按当前官方接口规则写入 `reasoning_effort` |
| Kimi / Moonshot | K2.5/2.6 使用思考开关；K2 Thinking、K2.7 不能关闭；K3 使用 low/high/max（xhigh → max），不接受关闭；未知别名应保留默认 |
| Kimi Coding | 直接移植 CPA 的 `thinking.type` / `thinking.effort` 写入与旧字段清理；与 Moonshot 开放平台分开。K3/K2.8 可映射到 max，其他型号最高 high |
| Doubao / 豆包 | `thinking.type`；已识别的 Seed 1.6、1.8、2 系列额外发送档位。`ep-` 接入点和旧型号只发送开关，须选择本身支持深度思考的模型 |
| MiniMax | 开启 `reasoning_split`，分离正文与结构化思考，保留 `reasoning_details`。M3 支持 adaptive/disabled；M2 系列不能关闭。不支持的关闭请求在本地提示，其他档位只表示启用 |
| GLM / 智谱 | `thinking.type`；GLM 5.2 及以上额外发送档位。旧型号仅控制开关；5.2 沿用服务映射；5.3 使用 low/high/max（medium → high、xhigh → max） |
| 通用 Chat Completions | 保留标准 `reasoning_effort`，不额外注入厂商开关；由上游网关决定支持范围 |

留空的思考等级遵循上游默认。MiniMax 的 `reasoning_split` 只改变输出格式，不增加思考预算。服务商不支持的档位不会触发自动重试。Responses、Claude、Gemini 原生接口仍按所选接口协议转换。

## 思考与工具历史

标准 `reasoning_content`、`reasoning`、Gemini `thought` 和 Claude 思考块转换到独立思考通道。MiniMax 只有 `reasoning_details` 时也能显示思考，累计快照不会重复拼接。普通正文里的 `<think>` 或 `<thinking>` 不会被正则删除；如果上游仍将它们写在正文，需让上游正确返回结构化思考字段。

插件保留完整工具消息。挟持模式通过 CPA 转换普通历史，并在内存中补存工具回合的原始思考字段和工具签名，按线路、凭据、模型、会话与用户历史匹配后回传。它不落盘，最多保留 256 轮、16 MB、30 分钟，关闭 App／挟持模式即释放。过期、压缩或重启后，不保证能恢复上游强制要求的原始签名，此时可开启新对话或使用支持持久回放的网关；不会编造签名填补缺失数据。

流式工具调用需要结束原因与完成标记齐全；断流、取消或错误不会自动再发送一次生成请求。超过 64 字符的 Chat 工具名按 CPA 算法映射，返回 Codex 时恢复原名称和命名空间。

## 来源与验证

直接移植代码及固定源提交见 [CPA 移植说明](../internal/cpacompat/README.md)。CPA 未提供全部厂商的独立适配，因此还对照以下官方文档补充实现（核对日期：2026-09-17）：

- [DeepSeek 思考模式](https://api-docs.deepseek.com/guides/thinking_mode)
- [Kimi 模型与接口说明](https://platform.moonshot.ai/docs/guide/kimi-k2-5-quickstart)
- [豆包深度思考](https://www.volcengine.com/docs/82379/1449737?lang=zh)
- [MiniMax OpenAI 兼容接口](https://platform.minimax.io/docs/api-reference/text-openai-api)
- [GLM 对话补全](https://docs.bigmodel.cn/api-reference/模型-api/对话补全)

回归使用本地模拟 HTTP 接口，覆盖各策略的参数、JSON、SSE、第二轮工具历史、凭据隔离与异常流。未调用真实付费模型，网关对新型号和参数的支持仍以其实际接口为准。
