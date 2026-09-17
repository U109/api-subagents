# 模型兼容

在「模型选择 → 编辑模型 → 兼容策略」设置，每个模型独立保存。该选项用于 **Chat Completions** 接口，同时作用于插件委派和挟持模式，不改写实际发送的模型 ID。

一个 worker（连接）可勾选多个厂商的模型，共用同一地址和 Key；开启挟持后，在 Codex 中直接切换具体模型，本地按本轮选择使用对应兼容策略和容量，无需创建多个 worker 或反复调整 App 默认模型。首次开启及修改模型列表后重启 Codex，已有列表中的模型切换无需重启。

默认「自动」只识别官方 API 主机名；自定义地址沿用通用 OpenAI 格式。通过 CPA 接入时选「CPA / 通用 OpenAI」，由 CPA 转换厂商参数；自建反向代理直接转发到厂商时，可指定对应策略。仅凭模型名称无法判断网关接受哪种协议。

| 策略 | 本地处理与边界 |
| --- | --- |
| Gemini | Chat 档位映射，原生 Gemini 另有 schema 清理和思考签名处理。Gemini 3 Flash 的“关闭”使用 minimal；3 Pro 使用 low，medium 折合 high；xhigh 折合 high，不能因此完全停止思考 |
| DeepSeek | `thinking.type` 控制开关；minimal/low → low，medium/high/xhigh → high，按当前官方接口规则写入 `reasoning_effort` |
| Kimi / Moonshot | K2.5/2.6 使用思考开关；K2 Thinking、K2.7 不能关闭；K3 使用 low/high/max（xhigh → max），不接受关闭；未知别名应保留默认 |
| Kimi Coding | 直接移植 CPA 的 `thinking.type` / `thinking.effort` 写入与旧字段清理；与 Moonshot 开放平台分开。K3/K2.8 可映射到 max，其他型号最高 high |
| Doubao / 豆包 | `thinking.type`；已识别的 Seed 1.6、1.8、2 系列额外发送档位。`ep-` 接入点和旧型号只发送开关，须选择本身支持深度思考的模型 |
| MiniMax | 开启 `reasoning_split`，分离正文与结构化思考，保留 `reasoning_details`。M3 支持 adaptive/disabled；M2 系列不能关闭。不支持的关闭请求在本地提示，其他档位只表示启用 |
| GLM / 智谱 | `thinking.type`；GLM 5.2 及以上额外发送档位。旧型号仅控制开关；5.2 沿用服务映射；5.3 使用 low/high/max（medium → high、xhigh → max） |
| CPA / 通用 OpenAI | 保留标准 `reasoning_effort`，不额外注入厂商开关；由上游网关决定支持范围 |

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
