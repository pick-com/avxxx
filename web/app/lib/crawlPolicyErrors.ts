// zh-CN text for PUT admin/sources/{id}/crawl-policy validation errors. The
// API answers stable English messages (internal/api/admin.go,
// internal/store/crawl.go, internal/catalog/proxy.go); the back office is
// zh-CN only. Unknown messages pass through unchanged. Pure (node --test).

const EXACT: Record<string, string> = {
  'invalid id': '来源 ID 无效',
  'unknown source': '来源不存在',
  'min_request_interval is required': '请填写最小请求间隔',
  'invalid interval': '间隔格式无效，请用 PostgreSQL interval 写法，例如「6 hours」「1 day」「00:00:05」',
  'budgets must not be negative': '预算不能为负数（0 表示不限）',
  'proxy_url too long': '出口代理设置过长',
  'proxy URL is not a valid URL': '出口代理不是有效的 URL',
  'proxy URL scheme must be http, https, socks5 or socks5h': '出口代理协议须为 http、https、socks5 或 socks5h',
  'proxy URL needs a host': '出口代理缺少主机名',
  'proxy URL must be scheme://[user:password@]host:port': '出口代理格式须为「协议://主机:端口」，不能带路径或参数',
};

const PREFIX: [string, string][] = [
  ['proxy_url env reference must name a ', '环境变量引用须为 env:CATALOG_PROXY_<名称>（仅限 A-Z、0-9、_）'],
  ['proxy_url must not contain credentials', '出口代理不能包含账号密码：把完整地址写入 CATALOG_PROXY_* 环境变量，这里填 env:变量名'],
];

/** The zh-CN message for a crawl-policy error detail; unknown text unchanged. */
export function crawlPolicyErrorText(detail: string): string {
  const d = detail.trim();
  if (EXACT[d]) return EXACT[d];
  for (const [prefix, zh] of PREFIX) if (d.startsWith(prefix)) return zh;
  return detail;
}
