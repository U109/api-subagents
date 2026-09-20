# 官方上下文容量

核对日期：2026-09-20。内置规格随 App 版本维护，前端提示与 Codex 模型目录共用 `internal/config/official_contexts.go` 中的数据；不会在模型请求期间查询文档或发送测试消息。

| 型号 | 自动容量（tokens） | 官方来源 |
| --- | ---: | --- |
| GPT-6 Astra | 1,050,000 | [OpenAI](https://developers.openai.com/api/docs/models/gpt-6-astra) |
| GPT-5.6 Luna / Sol / Terra | 1,050,000 | [Luna](https://developers.openai.com/api/docs/models/gpt-5.6-luna)、[Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol)、[Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra) |
| Gemini 3.8 Flash / 3.1 Pro Preview | 1,048,576 | [Flash](https://ai.google.dev/gemini-api/docs/models/gemini-3.8-flash)、[Pro Preview](https://ai.google.dev/gemini-api/docs/models/gemini-3.1-pro-preview) |
| DeepSeek V4 Flash / Pro | 1,000,000 | [DeepSeek](https://api-docs.deepseek.com/quick_start/pricing/) |
| Kimi K3 | 1,000,000 | [Moonshot](https://platform.moonshot.ai/docs/guide/models) |
| MiniMax M3 | 1,000,000 | [MiniMax](https://platform.minimax.io/docs/api-reference/text-anthropic-api) |
| MiniMax M2 / M2.1 / M2.5 / M2.7 | 204,800 | [MiniMax](https://platform.minimax.io/docs/api-reference/text-anthropic-api) |
| GLM-5.3 | 1,000,000 | [Z.ai](https://docs.z.ai/guides/llm/glm-5.3) |
| Claude Fable 5.1 / Opus 5 / Sonnet 5 | 1,000,000 | [Anthropic](https://platform.claude.com/docs/en/about-claude/models/overview) |
| Claude Haiku 4.5 | 200,000 | [Anthropic](https://platform.claude.com/docs/en/about-claude/models/overview) |

官方只标注 1M 而未给出精确整数时，按 1,000,000 保守取值。Gemini 使用官方输入上限，不再叠加输出 tokens。已明确对应的 Gemini low / medium / high 别名与 MiniMax highspeed 别名参考基础型号，界面会标明按别名参考；不会更改实际发送的模型 ID。

未知型号、未核实型号及自定义别名默认 **256K（256,000 tokens）**，不按厂商前缀推断。当前未核实容量的豆包等型号也遵循该规则；这不代表厂商已承诺支持 256K。

手动设置始终优先，适用于限制更小的网关或未收录的新模型。已有手动值不会自动删除；在「模型选择 → 编辑模型」点击「恢复自动」并保存，才会改用官方规格或未知型号默认值。更改后重启 Codex 重新加载目录。自动压缩阈值继续为所选容量的 90%。
