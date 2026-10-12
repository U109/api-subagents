import React, {useState} from 'react';
import '../relay-diagnostics-ui.js';

/** 最近请求只展示后端有界、脱敏的元数据，不把 HTTP 200 或接入成功误报为生成完成。 */
export function RequestDiagnostics({relay}) {
  const [message, setMessage] = useState('');
  const items = (relay?.recentRequests || []).slice(0, 20);
  /** 复制用户主动选择的诊断摘要；失败时保留可选择的纯文本，不读取配置、Key 或提示词。 */
  async function copy(item) {
    try {
      await navigator.clipboard.writeText(window.relayDiagnostics.summary(item));
      setMessage('已复制脱敏诊断信息');
    } catch {
      setMessage('无法访问剪贴板，请选择下方诊断信息手动复制。');
    }
  }
  return <div className="dialog-body request-diagnostics">
    <p>仅记录本次开启期间最近 20 条请求的状态、耗时和安全请求标识，不包含提示词、回答或凭据。</p>
    {!items.length ? <p role="status">尚无请求记录</p> : <ol className="request-diagnostics-list">{items.map(item => <li key={item.id}>
      <details><summary><strong>{window.relayDiagnostics.describe(item.outcome)[0]}</strong><span>{item.connection} · {item.model}</span></summary>
        <pre>{window.relayDiagnostics.summary(item)}</pre><button type="button" className="button button-secondary" aria-label={'复制 ' + item.connection + ' 的请求诊断'} onClick={() => copy(item)}>复制诊断</button>
      </details>
    </li>)}</ol>}
    <p role="status" aria-live="polite">{message}</p>
  </div>;
}
