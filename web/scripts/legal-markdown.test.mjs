import assert from 'node:assert/strict';
import test from 'node:test';
import {legalHeadings, legalMarkdownToHtml as html, parseInline, parseLegalMarkdown, safeHref} from '../app/lib/legalMarkdown.ts';

test('headings: # and ## are sections, ### and deeper are sub headings', () => {
  const b = parseLegalMarkdown('# Terms of use\n## Scope {#scope}\n### Detail\n#### Deeper');
  assert.deepEqual(
    b.map(x => [x.kind, x.id]),
    [
      ['h2', 'terms-of-use'],
      ['h2', 'scope'],
      ['h3', 'detail'],
      ['h3', 'deeper'],
    ],
  );
  assert.deepEqual(legalHeadings(b), [
    {id: 'terms-of-use', title: 'Terms of use'},
    {id: 'scope', title: 'Scope'},
  ]);
  assert.equal(html('#no-space'), '<p>#no-space</p>');
});

test('duplicate heading ids get a suffix', () => {
  assert.deepEqual(
    parseLegalMarkdown('## A\n## A').map(b => b.id),
    ['a', 'a-2'],
  );
});

test('paragraphs, bullet and numbered lists', () => {
  assert.equal(
    html('one\ntwo\n\n- a\n- b\n1. x\n2. y\n\nend'),
    '<p>one two</p>\n<ul><li>a</li><li>b</li></ul>\n<ol><li>x</li><li>y</li></ol>\n<p>end</p>',
  );
});

test('emphasis: bold, italic, nested; snake_case stays', () => {
  assert.equal(html('**bold** and *it* and __b__ and _i_'), '<p><strong>bold</strong> and <em>it</em> and <strong>b</strong> and <em>i</em></p>');
  assert.equal(html('**a *b* c**'), '<p><strong>a <em>b</em> c</strong></p>');
  assert.equal(html('user_id and file_name'), '<p>user_id and file_name</p>');
  assert.equal(html('2 * 3 * 4'), '<p>2 * 3 * 4</p>');
});

test('all HTML is escaped', () => {
  assert.equal(html('<script>alert(1)</script> & "x"'), '<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; &quot;x&quot;</p>');
  assert.equal(html('## <img src=x onerror=alert(1)>'), '<h2 id="img-src-x-onerror-alert-1">&lt;img src=x onerror=alert(1)&gt;</h2>');
  assert.equal(html('[<b>x</b>](/a)'), '<p><a href="/a">&lt;b&gt;x&lt;/b&gt;</a></p>');
});

test('links: http(s), mailto and relative only', () => {
  assert.equal(html('[site](https://example.com/a?b=1)'), '<p><a href="https://example.com/a?b=1" target="_blank" rel="noopener noreferrer">site</a></p>');
  assert.equal(html('[mail](mailto:legal@example.com)'), '<p><a href="mailto:legal@example.com">mail</a></p>');
  assert.equal(html('[p](/legal/privacy#cookies)'), '<p><a href="/legal/privacy#cookies">p</a></p>');
  assert.equal(html('[s](#scope)'), '<p><a href="#scope">s</a></p>');
  for (const bad of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,x', 'vbscript:x', '//evil.example', 'ftp://x']) {
    assert.equal(safeHref(bad), null, bad);
    assert.equal(html(`[x](${bad})`), '<p>x</p>', bad);
  }
  assert.equal(safeHref('https://x" onmouseover="a'), null);
  assert.equal(safeHref('privacy'), 'privacy');
  assert.equal(safeHref('../privacy'), '../privacy');
});

test('inline nodes are data, not markup', () => {
  assert.deepEqual(parseInline('a **b**'), [
    {kind: 'text', text: 'a '},
    {kind: 'strong', children: [{kind: 'text', text: 'b'}]},
  ]);
});
