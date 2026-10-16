import assert from 'node:assert/strict';
import test from 'node:test';
import {expandPlurals, pluralCount, scanPlurals} from '../app/lib/plural.ts';

const videos = '{n, plural, one {# video} other {# videos}} · {time}';

test('English picks one/other with Intl.PluralRules', () => {
  assert.equal(expandPlurals(videos, {n: 1, time: '2h'}, 'en'), '1 video · {time}');
  assert.equal(expandPlurals(videos, {n: 2}, 'en'), '2 videos · {time}');
  assert.equal(expandPlurals(videos, {n: 0}, 'en'), '0 videos · {time}');
  assert.equal(expandPlurals(videos, {n: '1,234'}, 'en'), '1,234 videos · {time}');
});

test('formatted strings: only integers select a category', () => {
  assert.equal(pluralCount('1'), 1);
  assert.equal(pluralCount('1,234'), 1234);
  assert.equal(pluralCount('1.234'), 1234);
  assert.equal(pluralCount('0.1'), null);
  assert.equal(pluralCount('1.0'), null);
  assert.equal(pluralCount('1.2K'), null);
  assert.equal(expandPlurals('{n, plural, one {# star} other {# stars}}', {n: '1.0'}, 'en'), '1.0 stars');
});

test('exact selectors win, other is the fallback, CJK has only other', () => {
  const s = '{n, plural, =0 {none} one {# item} other {# items}}';
  assert.equal(expandPlurals(s, {n: 0}, 'en'), 'none');
  assert.equal(expandPlurals(s, {n: 1}, 'ja'), '1 items');
  assert.equal(expandPlurals(s, {}, 'en'), '# items');
});

test('Spanish uses one for 1', () => {
  const s = '{n, plural, one {# día} other {# días}}';
  assert.equal(expandPlurals(s, {n: 1}, 'es'), '1 día');
  assert.equal(expandPlurals(s, {n: 5}, 'es'), '5 días');
});

test('malformed blocks throw in scan and pass through in expand', () => {
  assert.throws(() => scanPlurals('{n, plural, one {x}}'), /other/);
  assert.throws(() => scanPlurals('{n, plural, one {x} other {y}'), /unclosed/);
  assert.equal(expandPlurals('{n, plural, one {x}}', {n: 1}, 'en'), '{n, plural, one {x}}');
  assert.equal(scanPlurals('{a} and {b, plural, one {#} other {#s}}').length, 1);
});
