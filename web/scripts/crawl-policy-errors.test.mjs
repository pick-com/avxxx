// Crawl-policy errors in the zh-CN back office (app/lib/crawlPolicyErrors.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {crawlPolicyErrorText} from '../app/lib/crawlPolicyErrors.ts';

test('known API messages read in Chinese', () => {
  assert.equal(crawlPolicyErrorText('min_request_interval is required'), '请填写最小请求间隔');
  assert.match(crawlPolicyErrorText('invalid interval'), /^间隔格式无效/);
  assert.match(crawlPolicyErrorText('budgets must not be negative\n'), /^预算不能为负数/);
  assert.match(crawlPolicyErrorText('proxy URL needs a host'), /主机名/);
});

test('messages with a variable tail match by prefix', () => {
  assert.match(
    crawlPolicyErrorText(
      'proxy_url must not contain credentials: put the full URL in a CATALOG_PROXY_* environment variable and set proxy_url to env:<NAME>',
    ),
    /^出口代理不能包含账号密码/,
  );
  assert.match(crawlPolicyErrorText('proxy_url env reference must name a CATALOG_PROXY_* variable (A-Z, 0-9, _)'), /^环境变量引用/);
});

test('unknown messages pass through', () => {
  assert.equal(crawlPolicyErrorText('request failed'), 'request failed');
});
