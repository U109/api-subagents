# 模型价格与 Fast

## 默认价格

本版本内置 67 条公开报价，核对日期为 **2026-10-14**。价格单位为 **USD / 百万 Token**，采用付费标准文本请求的参考费率，不是套餐额度或供应商账单。

- OpenAI：GPT-6、GPT-5.6、GPT-5.5、GPT-5.4、GPT-5.2、GPT-5.1、GPT-5，以及官网仍列出的 GPT-4 / o 系列。
- Claude：官网列出的 Fable、Mythos、Opus、Sonnet、Haiku 型号；普通缓存创建默认采用 5 分钟缓存价格。
- Gemini：Gemini 4 Argon、3.8 Flash、3.1 Pro / Flash Lite、2.5 系列的付费标准文本费率。
- DeepSeek：官网列出的 Flash、V4 Pro 与明确保留的 Flash 兼容别名，采用高峰参考价格。官网低峰价格为高峰的一半；按实际时段或中国法定节假日使用时，可覆盖价格。

来源：

- [OpenAI API Pricing](https://developers.openai.com/api/docs/pricing)
- [Claude Pricing](https://platform.claude.com/docs/en/about-claude/pricing)
- [Gemini API Pricing](https://ai.google.dev/gemini-api/docs/pricing)
- [DeepSeek Models & Pricing](https://api-docs.deepseek.com/quick_start/pricing)

只匹配明确核实的模型 ID，不根据相似名称、显示名称或 `fast` 后缀猜价格。中转服务、私有模型及未核实型号可以手动填写；未定价不显示虚假的零费用。内置报价是版本发布时的快照，不会在后台访问官网或自动替换用户价格；供应商调整报价后应手动核对或更新应用。

## 模型级 Fast

在「我的连接 → 模型选择 → 模型设置」开启 Fast，先保存模型，再保存连接。取消弹窗不修改草稿。开关默认关闭，名称含 `fast` 也不会自动开启；已开启模型在连接与接入配置的模型列表显示雷电。

主对话接入已经开启时，保存连接不会自动改写正在运行的接入快照；还需在接入配置中明确更新接入。插件新任务读取已保存连接，运行中的任务保持原有参数与范围。

- Responses 与兼容 Chat 请求使用 `service_tier: "priority"`。OpenAI 官方说明 `priority` 与 `fast` 在支持型号上具有相同行为，使用前者兼容现有中转服务。
- Claude 原生请求使用 `speed: "fast"` 与 `anthropic-beta: fast-mode-2026-02-01`。
- Gemini 原生协议暂不支持此开关，界面明确禁用，不发送不符合协议的参数。
- Responses 压缩请求不附加 Fast 档位，未回报实际档位时按普通费率估算；关闭的模型不会沿用 Codex 传来的 Fast / Priority。

Fast 是否可用取决于上游模型、接口和账号，打开开关不保证一定提速，也不自动降低思考等级。供应商拒绝不支持的参数时保留真实错误，不尝试改为另一个模型或自动产生额外付费请求。

协议出处：[OpenAI Fast mode](https://developers.openai.com/api/docs/guides/priority-processing)、[Claude Fast mode](https://platform.claude.com/docs/en/build-with-claude/fast-mode)。

## 编辑与估算边界

「用量统计 → 价格统计」只展示**已保存连接中已选择的模型**，未选目录项或仅存在于历史记录的模型不会加入价格表。历史筛选与历史记录仍保留，保存价格不删除隐藏历史项。

- 普通与 Fast 的四项单价独立保存，允许高精度小数与显式零；部分填写报错，不把缺项补零。
- 支持官网明确列出的长上下文阈值及单价；阈值指统一输入 Token，恰好等于阈值仍使用短上下文价。
- 用户保存的覆盖优先于内置价格。普通费率全空会删除该覆盖，已知型号恢复内置默认；未知型号保持未定价。
- Fast 价格全空表示没有 Fast 价格；不会把普通费率或零价当成 Fast 价格。
- 每次上游尝试在开始时取得价格快照，重试分别记录；修改价格不会重新计算历史请求。
- 上游明确回报普通速度时按普通费率结算；未回报档位时按请求开关估算，不声称已确认提速。
- 缺少必要用量、缓存明细、Fast 价格或长上下文价格时，费用保持未知。
- 不包含 Gemini 音频差价、缓存存储、搜索、工具收费、区域加价、Claude 1 小时缓存创建差价及供应商其他附加收费。

**实际收费以供应商账单为准。** 不为了补充用量而重发请求，不记录提示词、回答、Key、授权头或上游错误正文。
