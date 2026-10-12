let token = location.hash.slice(1) || sessionStorage.getItem('setup-token') || '';
if (location.hash) {sessionStorage.setItem('setup-token', token); history.replaceState(null, '', location.pathname + location.search);}

/** 只调用固定 Go 绑定或带令牌的回环 API；响应失败保留错误消息，不记录配置和凭据。 */
export async function api(route, body, signal) {
  if (window.desktopApp) return window.desktopApp.api(route, body);
  const response = await fetch(route, {method: body === undefined ? 'GET' : 'POST', headers: {'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body), signal});
  const value = await response.json();
  if (!response.ok || value.error) throw new Error(value.error || '配置操作失败，请重试。');
  return value;
}

/** 桌面专属操作始终走固定绑定，浏览器入口不会模拟成功或发起任意系统命令。 */
export async function desktopAction(name, ...args) {
  if (typeof window.desktopApp?.[name] !== 'function') throw new Error('此操作请在 API Subagents 桌面程序中使用。');
  return window.desktopApp[name](...args);
}
