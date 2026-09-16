const maxResponseBytes = 8 * 1024 * 1024;

/** 创建增量 SSE 解码器；网络分块可以切断 UTF-8、CRLF 或一条事件，accept 只接收组装后的事件。 */
function eventDecoder(accept) {
  const decoder = new TextDecoder();
  let pending = '',
    event = '',
    data = [];
  /** 消费一行 SSE 字段，空行时派发多行 data，忽略注释心跳。 */
  const line = (value) => {
    if (!value) {
      if (data.length) accept(event, data.join('\n'));
      event = '';
      data = [];
    } else if (!value.startsWith(':')) {
      const colon = value.indexOf(':');
      const field = colon < 0 ? value : value.slice(0, colon);
      const text = colon < 0 ? '' : value.slice(colon + 1).replace(/^ /, '');
      if (field === 'event') event = text;
      if (field === 'data') data.push(text);
    }
  };
  return {
    /** 追加一个网络字节块；final 用于 EOF 时刷新解码器与未带结尾空行的事件。 */
    push(bytes, final = false) {
      pending += decoder.decode(bytes, {stream: !final});
      let match;
      while ((match = /\r\n|\r|\n/.exec(pending))) {
        if (!final && match[0] === '\r' && match.index === pending.length - 1) break;
        line(pending.slice(0, match.index));
        pending = pending.slice(match.index + match[0].length);
      }
      if (final && pending) {
        line(pending);
        pending = '';
      }
      if (final) line('');
    },
  };
}

/** 按协议累积正文、工具参数和签名，直到明确完成事件；中途断流不能产出可执行调用。 */
function streamResult(protocol) {
  let done = false,
    result,
    finishReason;
  const message = {role: 'assistant', content: ''};
  const calls = new Map(),
    blocks = new Map(),
    parts = [];
  let claudeMessage;
  return {
    /** 收到明确完成事件后可停止读取连接，无须等待服务端关闭 socket。 */
    get done() {
      return done;
    },
    /** 合并一个事件；错误只给出固定提示，避免把服务端原文或凭据带入父模型。 */
    accept(event, text) {
      if (done) return;
      if (text.trim() === '[DONE]') {
        if (protocol !== 'compatible' || !finishReason) throw new Error('模型流缺少完整结束信息。');
        message.tool_calls = [...calls.entries()].sort(([a], [b]) => a - b).map(([, call]) => call);
        if (!message.tool_calls.length) delete message.tool_calls;
        if (!message.content && message.tool_calls) message.content = null;
        result = {choices: [{message, finish_reason: finishReason}]};
        done = true;
        return;
      }
      let data;
      try {
        data = JSON.parse(text);
      } catch {
        throw new Error('模型流包含无效 JSON。');
      }
      if (!data || typeof data !== 'object') throw new Error('模型流事件格式无效。');
      const type = data.type || event;
      if (data.error || type === 'error' || type === 'response.failed')
        throw new Error('模型流报告请求失败，请检查服务状态、模型和额度。');
      if (protocol === 'responses') {
        // completed/incomplete 事件携带完整 output，包括下一轮必需的加密推理内容。
        if (type === 'response.completed' || type === 'response.incomplete') {
          result = data.response;
          if (!result || !Array.isArray(result.output)) throw new Error('Responses 流缺少完整 output。');
          if (type === 'response.incomplete') result.status = 'incomplete';
          done = true;
        }
      } else if (protocol === 'anthropic') {
        if (type === 'message_start') claudeMessage = {...data.message, content: []};
        if (type === 'content_block_start') {
          if (!Number.isInteger(data.index) || data.index < 0 || blocks.has(data.index))
            throw new Error('Claude 流内容索引无效。');
          blocks.set(data.index, {
            block: {...data.content_block},
            json: '',
            hasJson: false,
            stopped: false,
          });
        }
        if (type === 'content_block_delta' || type === 'content_block_stop') {
          const item = blocks.get(data.index);
          if (!item || item.stopped) throw new Error('Claude 流内容块顺序无效。');
          if (type === 'content_block_stop') {
            if (item.hasJson) {
              try {
                item.block.input = JSON.parse(item.json);
              } catch {
                throw new Error('Claude 流工具参数不完整。');
              }
            }
            item.stopped = true;
          } else {
            const delta = data.delta || {};
            const field = {text_delta: 'text', thinking_delta: 'thinking', signature_delta: 'signature'}[
              delta.type
            ];
            if (field) item.block[field] = (item.block[field] || '') + (delta[field] || '');
            if (delta.type === 'input_json_delta') {
              item.hasJson = true;
              item.json += delta.partial_json || '';
            }
          }
        }
        if (type === 'message_delta' && claudeMessage) Object.assign(claudeMessage, data.delta);
        if (type === 'message_stop') {
          if (!claudeMessage?.stop_reason || [...blocks.values()].some((item) => !item.stopped))
            throw new Error('Claude 流缺少完整结束信息。');
          result = {
            ...claudeMessage,
            content: [...blocks.entries()].sort(([a], [b]) => a - b).map(([, item]) => item.block),
          };
          done = true;
        }
      } else if (protocol === 'gemini') {
        const candidate = data.candidates?.find((item) => (item.index ?? 0) === 0);
        for (const part of candidate?.content?.parts || []) {
          const previous = parts.at(-1);
          // 文本分片应连接为原文；带签名或工具调用的 part 保留原结构供下一轮回填。
          if (
            typeof part.text === 'string' &&
            typeof previous?.text === 'string' &&
            Boolean(part.thought) === Boolean(previous.thought) &&
            !part.thoughtSignature &&
            !previous.thoughtSignature
          )
            previous.text += part.text;
          else parts.push({...part});
        }
        if (candidate?.finishReason) {
          result = {candidates: [{...candidate, content: {role: 'model', parts}}]};
          done = true;
        }
        if (data.promptFeedback?.blockReason) throw new Error('Gemini 请求被过滤，未返回可用内容。');
      } else {
        const choice = data.choices?.find((item) => (item.index ?? 0) === 0);
        if (!choice) return; // 部分服务在结束前单独发送 usage。
        const delta = choice.delta || {};
        for (const field of ['content', 'refusal', 'reasoning_content', 'reasoning']) {
          if (typeof delta[field] === 'string') message[field] = (message[field] || '') + delta[field];
        }
        for (const part of delta.tool_calls || []) {
          if (!Number.isInteger(part.index) || part.index < 0 || part.index >= 24)
            throw new Error('模型流工具调用索引无效或超过 24 次。');
          if (!calls.has(part.index))
            calls.set(part.index, {id: '', type: 'function', function: {name: '', arguments: ''}});
          const call = calls.get(part.index);
          if (part.id) call.id += part.id;
          if (part.function?.name) call.function.name += part.function.name;
          if (part.function?.arguments) call.function.arguments += part.function.arguments;
        }
        if (choice.finish_reason) finishReason = choice.finish_reason;
      }
    },
    /** 校验完整性并返回与非流式接口相同的响应结构，供 provider 层统一处理。 */
    finish() {
      if (!done) throw new Error('模型流在完成前中断，未执行本轮未完成的工具调用。');
      for (const call of calls.values())
        if (!call.id || !call.function.name) throw new Error('模型流工具调用缺少 ID 或名称。');
      return result;
    },
  };
}

/** 接收 SSE 或兼容 JSON；首次数据与空闲间隔分别计时，父任务信号始终可以取消，最多接收 8 MB。 */
export async function fetchReply(spec, profile, signal, fetchImpl = fetch, onProgress = () => {}) {
  const firstMs = (profile.firstResponseTimeoutSeconds ?? 180) * 1000;
  const idleMs = (profile.streamIdleTimeoutSeconds ?? 120) * 1000;
  const controller = new AbortController();
  const combined = AbortSignal.any([signal, controller.signal]);
  let timer,
    reader,
    receivedBytes = 0,
    events = 0,
    firstByteAt,
    lastActivityAt;
  const requestStartedAt = new Date().toISOString();
  /** 只报告时间与计数，不把尚未完成的正文或推理片段放进父模型上下文。 */
  const progress = (phase) =>
    onProgress({
      phase,
      requestStartedAt,
      firstByteAt,
      lastActivityAt,
      receivedBytes,
      events,
      requestElapsedMs: Date.now() - Date.parse(requestStartedAt),
    });
  /** 重置当前阶段的计时器；区分未开始响应和接收中断，便于配置与定位问题。 */
  const arm = (ms, first) => {
    clearTimeout(timer);
    timer = setTimeout(
      () =>
        controller.abort(
          Object.assign(
            new Error(
              first
                ? `等待首个响应数据超过 ${ms / 1000} 秒。可在更多设置中调整等待时间。`
                : `接收响应时连续 ${ms / 1000} 秒没有新数据。可在更多设置中调整中断等待时间。`,
            ),
            {code: first ? 'FIRST_RESPONSE_TIMEOUT' : 'STREAM_IDLE_TIMEOUT'},
          ),
        ),
      ms,
    );
    timer.unref?.();
  };
  let rejectAbort;
  const aborted = new Promise((_, reject) => {
    rejectAbort = reject;
  });
  /** 唤醒正在等待的请求/读取并释放 body；即使注入的 fetch 忽略信号也能及时退出。 */
  const onAbort = () => {
    rejectAbort(combined.reason);
    if (reader) void reader.cancel().catch(() => {});
  };
  combined.addEventListener('abort', onAbort, {once: true});
  try {
    combined.throwIfAborted();
    progress('waiting_response');
    arm(firstMs, true);
    const request = Promise.resolve(
      fetchImpl(spec.url, {
        method: 'POST',
        redirect: 'error',
        headers: spec.headers,
        body: JSON.stringify(spec.body),
        signal: combined,
      }),
    ).then((res) => {
      if (combined.aborted) {
        void res.body?.cancel().catch(() => {});
        combined.throwIfAborted();
      }
      return res;
    });
    const res = await Promise.race([request, aborted]);
    if (!res.ok) {
      void res.body?.cancel().catch(() => {});
      throw Object.assign(
        new Error(`模型 API 返回 HTTP ${res.status}。请检查地址、Key、模型 ID 和额度。`),
        {status: res.status},
      );
    }
    if (!res.body) throw new Error('模型 API 返回空响应。');
    const streaming = (res.headers.get('content-type') || '')
      .toLowerCase()
      .includes('text/event-stream');
    const accumulator = streamResult(profile.protocol);
    const decoder = eventDecoder((event, data) => {
      events++;
      accumulator.accept(event, data);
    });
    const chunks = [];
    reader = res.body.getReader();
    while (true) {
      const {done, value} = await Promise.race([reader.read(), aborted]);
      combined.throwIfAborted();
      if (done) break;
      if (!value.length) continue;
      receivedBytes += value.length;
      if (receivedBytes > maxResponseBytes) throw new Error('模型响应超过 8 MB。');
      lastActivityAt = new Date().toISOString();
      firstByteAt ||= lastActivityAt;
      arm(idleMs, false); // 数据、推理片段和 SSE 心跳均表明连接仍活跃。
      if (streaming) decoder.push(value);
      else chunks.push(value); // 兼容忽略 stream 参数、直接返回 JSON 的网关，不重发请求。
      progress(streaming ? 'streaming' : 'receiving_json');
      if (streaming && accumulator.done) break;
    }
    let result;
    if (streaming) {
      decoder.push(new Uint8Array(), true);
      result = accumulator.finish();
    } else {
      try {
        result = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      } catch {
        throw new Error('API 没有返回有效 JSON 或 SSE 流，请检查接口类型。');
      }
    }
    progress('response_received');
    return result;
  } catch (error) {
    progress('request_failed');
    throw combined.aborted ? combined.reason : error;
  } finally {
    clearTimeout(timer);
    combined.removeEventListener('abort', onAbort);
    if (reader) void reader.cancel().catch(() => {});
  }
}
