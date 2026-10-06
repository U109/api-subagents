// 最近请求面板只接收 Go 的脱敏元数据；所有动态内容用 textContent 呈现，不展示请求或回答正文。
(() => {
  const outcomes = {
    receiving: ['正在接收', 'neutral'],
    completed: ['生成完成', 'success'],
    http_completed: ['非流响应已接收', 'neutral'],
    upstream_failed: ['上游报告生成失败', 'warning'],
    incomplete: ['上游未完整生成', 'warning'],
    unexpected_eof: ['响应结束但缺少完成事件', 'warning'],
    upstream_read_error: ['读取上游时连接中断', 'warning'],
    upstream_connect_error: ['无法连接上游', 'warning'],
    first_byte_timeout: ['等待首个响应字节超时', 'warning'],
    stream_idle_timeout: ['响应流空闲超时', 'warning'],
    request_timeout: ['请求超过总时限', 'warning'],
    client_cancelled: ['客户端取消或断开', 'neutral'],
    downstream_write_error: ['向客户端发送失败', 'warning'],
    http_auth_error: ['上游拒绝认证或权限', 'warning'],
    http_error: ['上游返回 HTTP 错误', 'warning'],
    invalid_request: ['请求参数无效', 'warning'],
    observation_unknown: ['无法确认响应完成状态', 'warning'],
  };
  let signature = '';
  let previous = new Map();

  /** 将固定结果枚举转为文案和语义颜色，HTTP 200 或未知结果不会被显示为生成成功。 */
  function describe(outcome) {
    return outcomes[outcome] || outcomes.observation_unknown;
  }

  /** 生成便于排障的纯文本摘要，只包含后端明确允许的元数据字段。 */
  function summary(item) {
    const lines = [
      '连接：' + item.connection,
      '模型：' + item.model,
      '开始时间：' + item.startedAt,
      '结果：' + describe(item.outcome)[0],
      'HTTP：' + (item.httpStatus || '未收到响应头'),
      '上游请求 ID：' + (item.upstreamRequestId || '上游未提供安全可显示的标识'),
      '本地请求 ID：' + item.id,
      '首字节：' + (item.firstByteMs >= 0 ? item.firstByteMs + ' ms' : '未收到'),
      '耗时：' + item.durationMs + ' ms',
      '接收字节：' + item.bytesReceived,
    ];
    return lines.join('\n');
  }

  /** 创建仅含文本的 DOM 节点，避免模型名或请求标识被解释成 HTML。 */
  function textNode(tag, className, text) {
    const element = document.createElement(tag);
    element.className = className;
    element.textContent = text;
    return element;
  }

  /** 复制用户选定的脱敏摘要，失败时保留可手动选择的请求信息，不读取配置或凭据。 */
  async function copySummary(item) {
    try {
      await navigator.clipboard.writeText(summary(item));
      window.notices?.show('request-diagnostics', '已复制脱敏诊断信息', 'success');
    } catch {
      window.notices?.show('request-diagnostics', '无法访问剪贴板，请手动选择请求信息复制', 'warning');
    }
  }

  /** 渲染独立的最近请求记录，不把路由已接入等同于生成成功，也不让旧请求覆盖当前连接。 */
  function render(relay) {
    const panel = document.getElementById('relay-diagnostics');
    if (!panel) return;
    const items = (relay?.recentRequests || []).slice(0, 20);
    panel.hidden = !relay?.enabled && items.length === 0;
    const nextSignature = JSON.stringify(items);
    if (nextSignature === signature) return;
    signature = nextSignature;
    const badge = document.getElementById('relay-diagnostics-summary');
    const latest = items[0];
    const [label, kind] = latest ? describe(latest.outcome) : ['尚无请求', 'neutral'];
    badge.textContent = label;
    badge.dataset.kind = kind;
    const list = document.getElementById('relay-diagnostics-list');
    const focusID = list.contains(document.activeElement) ? document.activeElement.dataset.requestId : null;
    const rows = items.map(item => {
      const [message, tone] = describe(item.outcome);
      const row = textNode('li', 'request-result', '');
      row.dataset.kind = tone;
      const heading = textNode('div', 'request-result-heading', '');
      heading.append(textNode('strong', 'request-result-status', message));
      const copy = textNode('button', 'text-button request-copy', '复制诊断');
      copy.type = 'button';
      copy.dataset.requestId = item.id;
      copy.setAttribute('aria-label', '复制 ' + item.connection + ' 的请求诊断');
      copy.addEventListener('click', () => copySummary(item));
      heading.append(copy);
      const started = new Date(item.startedAt);
      const timestamp = Number.isNaN(started.getTime()) ? '' : started.toLocaleString();
      const meta = item.connection + ' · ' + item.model + ' · ' + timestamp;
      const metrics = (item.httpStatus ? 'HTTP ' + item.httpStatus : '等待响应头') + ' · ' + (item.outcome === 'receiving' ? '接收中' : (item.durationMs / 1000).toFixed(1) + ' 秒');
      const requestID = item.upstreamRequestId ? '上游请求 ID：' + item.upstreamRequestId : '本地请求 ID：' + item.id + '（上游未提供安全可显示的标识）';
      row.append(heading, textNode('p', 'request-result-meta', meta), textNode('p', 'request-result-metrics', metrics), textNode('code', 'request-result-id', requestID));
      return row;
    });
    list.replaceChildren(...rows);
    if (focusID) {
      [...list.querySelectorAll('button')].find(button => button.dataset.requestId === focusID)?.focus();
    }
    document.getElementById('relay-diagnostics-empty').hidden = items.length > 0;
    const newlyFailed = items.find(item => previous.get(item.id) === 'receiving' && describe(item.outcome)[1] === 'warning');
    if (newlyFailed) window.notices?.show('request-diagnostics', newlyFailed.connection + '：' + describe(newlyFailed.outcome)[0] + '，可展开“最近请求”查看诊断', 'warning');
    previous = new Map(items.map(item => [item.id, item.outcome]));
  }

  window.relayDiagnostics = {render, describe, summary};
})();
