import {test} from 'node:test';
import assert from 'node:assert/strict';
import {codeMatchKey, filterHistory, matchesHistory, normalizeHistoryQuery} from '../app/lib/historyFilter.ts';

const rows = [
  {slug: 'abc-00123', title: 'Summer Beach Story'},
  {slug: 'xyz-456', title: '夏の海物語', performers: ['葵はな', 'Hana Aoi']},
  {slug: 'night-office', title: 'Office Night', codes: ['DEF-789']},
];
const slugs = q => filterHistory(rows, q).map(r => r.slug);

test('normalizeHistoryQuery folds width, case and spaces', () => {
  assert.equal(normalizeHistoryQuery('  ＡＢＣ   Beach '), 'abc beach');
  assert.equal(normalizeHistoryQuery(''), '');
});

test('codeMatchKey mirrors SQL code_match_key', () => {
  assert.equal(codeMatchKey('abc-00123'), 'ABC-123');
  assert.equal(codeMatchKey('ABC123'), 'ABC-123');
  assert.equal(codeMatchKey('heyzo-0000'), 'HEYZO-0');
  assert.equal(codeMatchKey('1pon-020121r'), '1PON-20121-R');
  assert.equal(codeMatchKey('foo'), 'FOO');
});

test('filterHistory: empty query keeps everything', () => {
  assert.deepEqual(slugs(''), ['abc-00123', 'xyz-456', 'night-office']);
  assert.deepEqual(slugs('   '), ['abc-00123', 'xyz-456', 'night-office']);
});

test('filterHistory: title, performer, slug', () => {
  assert.deepEqual(slugs('beach'), ['abc-00123']);
  assert.deepEqual(slugs('BEACH'), ['abc-00123']);
  assert.deepEqual(slugs('海物語'), ['xyz-456']);
  assert.deepEqual(slugs('hana'), ['xyz-456']);
  assert.deepEqual(slugs('葵'), ['xyz-456']);
  assert.deepEqual(slugs('office'), ['night-office']);
});

test('filterHistory: product codes', () => {
  assert.deepEqual(slugs('abc123'), ['abc-00123']); // code_match_key
  assert.deepEqual(slugs('ABC-001'), ['abc-00123']); // prefix
  assert.deepEqual(slugs('def789'), ['night-office']); // stored code
  assert.deepEqual(slugs('xyz 456'), ['xyz-456']);
});

test('filterHistory: no match', () => {
  assert.deepEqual(slugs('nothing'), []);
  assert.deepEqual(slugs('999'), []);
  assert.equal(matchesHistory(rows[0], ''), true);
});
