import {redact} from './config.mjs';
import {fetchReply} from './streaming.mjs';

/** 接受 API 根地址或完整端点，避免重复追加路径。 */
function endpoint(base, suffix) {
  return base.endsWith('/' + suffix) ? base : base + '/' + suffix;
}
/** 按厂商协议创建首条用户消息；Gemini 使用 parts，其余协议使用 content。 */
export function initialState(profile, prompt) {
  if (profile.protocol === 'gemini') return [{role: 'user', parts: [{text: prompt}]}];
  return [{role: 'user', content: prompt}];
}
/** 在已完成任务的历史中追加后续要求，保留原有工具调用和签名。 */
export function addUser(profile, state, prompt) {
  state.push(...initialState(profile, prompt));
}
/** 把统一模型配置、历史和工具定义转换为厂商 HTTP 请求，默认启用流式生成。 */
function requestSpec(p, history, system, tools) {
  // 只在边界转换各服务的格式；任务循环始终使用同一套工具定义。
  const headers = {'content-type': 'application/json'};
  const streaming = p.stream !== false;
  headers.accept = streaming ? 'text/event-stream, application/json' : 'application/json';
  const functions = tools.map((t) => ({name: t.name, description: t.description, parameters: t.schema}));
  if (p.protocol === 'anthropic') {
    headers['x-api-key'] = p.apiKey;
    headers['anthropic-version'] = '2023-06-01';
    return {
      url: endpoint(p.baseUrl, 'messages'),
      headers,
      body: {
        model: p.model,
        stream: streaming,
        max_tokens: p.maxTokens,
        system,
        messages: history,
        ...(tools.length
          ? {
              tools: tools.map((t) => ({
                name: t.name,
                description: t.description,
                input_schema: t.schema,
              })),
            }
          : {}),
      },
    };
  }
  if (p.protocol === 'gemini') {
    headers['x-goog-api-key'] = p.apiKey;
    return {
      url: endpoint(
        p.baseUrl.replace(/\/models\/[^/]+:(?:streamGenerateContent|generateContent)$/, ''),
        `models/${encodeURIComponent(p.model.replace(/^models\//, ''))}:${streaming ? 'streamGenerateContent?alt=sse' : 'generateContent'}`,
      ),
      headers,
      body: {
        systemInstruction: {parts: [{text: system}]},
        contents: history,
        generationConfig: {maxOutputTokens: p.maxTokens},
        ...(tools.length
          ? {
              tools: [
                {
                  functionDeclarations: functions.map((f) => ({
                    name: f.name,
                    description: f.description,
                    parametersJsonSchema: f.parameters,
                  })),
                },
              ],
            }
          : {}),
      },
    };
  }
  if (p.apiKey) headers.authorization = `Bearer ${p.apiKey}`;
  if (p.protocol === 'responses') {
    return {
      url: endpoint(p.baseUrl, 'responses'),
      headers,
      body: {
        model: p.model,
        stream: streaming,
        instructions: system,
        input: history,
        store: false,
        include: ['reasoning.encrypted_content'],
        max_output_tokens: p.maxTokens,
        ...(tools.length
          ? {tools: functions.map((f) => ({type: 'function', ...f, strict: false}))}
          : {}),
      },
    };
  }
  return {
    url: endpoint(p.baseUrl, 'chat/completions'),
    headers,
    body: {
      model: p.model,
      stream: streaming,
      messages: [{role: 'system', content: system}, ...history],
      max_tokens: p.maxTokens,
      ...(tools.length ? {tools: functions.map((f) => ({type: 'function', function: f}))} : {}),
    },
  };
}
/** 读取有大小与超时限制的普通 JSON，供模型目录查询使用；拒绝携带凭据跟随重定向。 */
export async function fetchJSON(spec, signal, fetchImpl = fetch) {
  // 模型目录仍是短 JSON 请求；生成请求由 fetchReply 使用流式分阶段超时。
  const res = await fetchImpl(spec.url, {
    method: spec.method || 'POST',
    redirect: 'error',
    headers: spec.headers,
    ...(spec.body === undefined ? {} : {body: JSON.stringify(spec.body)}),
    signal: AbortSignal.any([signal, AbortSignal.timeout(120000)]),
  });
  if (!res.ok) {
    await res.body?.cancel();
    throw Object.assign(
      new Error(`模型 API 返回 HTTP ${res.status}。请检查地址、Key、模型 ID 和额度。`),
      {
        status: res.status,
      },
    );
  }
  if (!res.body) throw new Error('模型 API 返回空响应。');
  const reader = res.body.getReader();
  const chunks = [];
  let size = 0;
  while (true) {
    const {done, value} = await reader.read();
    if (done) break;
    size += value.length;
    if (size > 8 * 1024 * 1024) {
      await reader.cancel();
      throw new Error('模型响应超过 8 MB。');
    }
    chunks.push(value);
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } catch {
    throw new Error('模型列表接口没有返回有效 JSON，请检查接口地址和类型。');
  }
}
/** 执行一轮模型请求并统一为正文与工具调用；仅在响应完整、未截断时提交历史。 */
export async function turn(profile, state, system, tools, signal, fetchImpl = fetch, onProgress) {
  const spec = requestSpec(profile, state, system, tools);
  let data;
  try {
    data = await fetchReply(spec, profile, signal, fetchImpl, onProgress);
  } catch (error) {
    if (signal.aborted) throw signal.reason;
    throw Object.assign(new Error(redact(error.message, [profile])), {code: error.code});
  }
  const additions = [];
  let calls = [],
    text = '',
    truncated = false;
  if (profile.protocol === 'responses') {
    if (data.error || data.status === 'failed') throw new Error('Responses API 报告请求失败。');
    const output = data.output;
    if (!Array.isArray(output)) throw new Error('API 缺少 Responses output；请检查接口类型。');
    additions.push(...output);
    calls = output
      .filter((x) => x.type === 'function_call')
      .map((x) => ({id: x.call_id, name: x.name, args: x.arguments}));
    text = output
      .filter((x) => x.type === 'message')
      .flatMap((x) => x.content || [])
      .filter((x) => x.type === 'output_text' || x.type === 'refusal')
      .map((x) => x.text || x.refusal)
      .join('\n');
    truncated = data.status === 'incomplete';
  } else if (profile.protocol === 'anthropic') {
    if (!Array.isArray(data.content)) throw new Error('API 缺少 Claude content；请检查接口类型。');
    additions.push({role: 'assistant', content: data.content});
    calls = data.content
      .filter((x) => x.type === 'tool_use')
      .map((x) => ({id: x.id, name: x.name, args: x.input}));
    text = data.content
      .filter((x) => x.type === 'text')
      .map((x) => x.text)
      .join('\n');
    truncated = data.stop_reason === 'max_tokens';
  } else if (profile.protocol === 'gemini') {
    const candidate = data.candidates?.[0];
    if (!candidate?.content?.parts)
      throw new Error('Gemini 未返回可用内容；可能被过滤或接口类型不匹配。');
    // Preserve original parts including thoughtSignature for subsequent Gemini tool turns.
    additions.push(candidate.content);
    calls = candidate.content.parts
      .filter((x) => x.functionCall)
      .map((x) => ({id: x.functionCall.id, name: x.functionCall.name, args: x.functionCall.args || {}}));
    text = candidate.content.parts
      .filter((x) => x.text && !x.thought)
      .map((x) => x.text)
      .join('\n');
    truncated = candidate.finishReason === 'MAX_TOKENS';
  } else {
    const choice = data.choices?.[0];
    const message = choice?.message;
    if (!message) throw new Error('API 缺少 Chat Completions choices；请检查接口类型。');
    additions.push(message);
    calls = (message.tool_calls || []).map((x) => ({
      id: x.id,
      name: x.function?.name,
      args: x.function?.arguments,
    }));
    text = typeof message.content === 'string' ? message.content : message.refusal || '';
    truncated = choice.finish_reason === 'length';
  }
  if (truncated) throw new Error('模型输出被截断。请提高该模型的最大输出长度，或缩小任务范围。');
  if (calls.length > 24) throw new Error('单轮工具调用超过 24 次。请缩小任务。');
  state.push(...additions);
  return {calls, text};
}
/** 以原调用 ID 回填文件工具结果，维持各厂商下一轮所要求的消息结构。 */
export function addResults(profile, state, results) {
  // 工具结果必须按原调用 ID 回填，下一轮模型才能继续同一段任务上下文。
  if (profile.protocol === 'anthropic')
    state.push({
      role: 'user',
      content: results.map((r) => ({
        type: 'tool_result',
        tool_use_id: r.id,
        content: JSON.stringify(r.output),
      })),
    });
  else if (profile.protocol === 'gemini')
    state.push({
      role: 'user',
      parts: results.map((r) => ({
        functionResponse: {name: r.name, ...(r.id ? {id: r.id} : {}), response: r.output},
      })),
    });
  else if (profile.protocol === 'responses')
    state.push(
      ...results.map((r) => ({
        type: 'function_call_output',
        call_id: r.id,
        output: JSON.stringify(r.output),
      })),
    );
  else
    state.push(
      ...results.map((r) => ({role: 'tool', tool_call_id: r.id, content: JSON.stringify(r.output)})),
    );
}
/** 发送受 30 秒及 1024 输出 tokens 限制的连通性测试，不读取项目或验证工具能力。 */
export async function probe(profile) {
  const state = initialState(profile, 'Reply with OK.');
  const answer = await turn(
    {...profile, maxTokens: Math.min(1024, profile.maxTokens)},
    state,
    'This is a connection test.',
    [],
    AbortSignal.timeout(30000),
  );
  return {ok: true, reply: redact(answer.text, [profile]).slice(0, 500)};
}
