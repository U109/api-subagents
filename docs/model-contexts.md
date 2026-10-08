# 上下文容量与旧版规格参考

App 0.5.5 起，只使用模型服务目录明确声明的容量，并取声明值与 **256,000 tokens 的较小值**；不按模型 ID 或厂商名称推测。目录未声明容量时显示待确认，暂用 256K 并保留旧手动配置。已知目录容量不能手动扩大；刷新目录、保存并重启 Codex 后生效。

以下是旧版于 2026-09-20 记录的规格参考，不代表当前上游实际支持，也不再决定新版自动容量。模型请求期间不会联网查规格或发送验证消息。

| 型号 | 已记录官方规格（tokens） | 官方来源 |
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

新版自动压缩阈值继续为实际采用容量的 90%。服务目录的声明和网关限制可能不同，应以用户实际使用的上游为准；目录缺失容量不代表上游已承诺支持 256K。
