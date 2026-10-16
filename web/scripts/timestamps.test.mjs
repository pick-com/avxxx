import {test} from 'node:test';
import assert from 'node:assert/strict';
import {splitTimestamps, timestampSeconds} from '../app/lib/timestamps.ts';

test('timestampSeconds reads m:ss and h:mm:ss', () => {
  assert.equal(timestampSeconds('0:00'), 0);
  assert.equal(timestampSeconds('12:34'), 754);
  assert.equal(timestampSeconds('1:02:03'), 3723);
  assert.equal(timestampSeconds('125:00'), 7500);
  assert.equal(timestampSeconds('1:60'), undefined);
  assert.equal(timestampSeconds('1:2'), undefined);
  assert.equal(timestampSeconds('1:61:00'), undefined);
});

test('splitTimestamps keeps the text around the times', () => {
  assert.deepEqual(splitTimestamps('看 12:34 和 1:02:03！'), ['看 ', {text: '12:34', seconds: 754}, ' 和 ', {text: '1:02:03', seconds: 3723}, '！']);
  assert.deepEqual(splitTimestamps('12:34'), [{text: '12:34', seconds: 754}]);
  assert.deepEqual(splitTimestamps('no times here'), ['no times here']);
  assert.deepEqual(splitTimestamps(''), []);
});

test('splitTimestamps ignores look-alikes', () => {
  for (const s of ['16:9', 'a1:23', '12:34:56:78', '1:23.5', 'v1:23', '10:30am', 'http://x:8080', '1:2:3']) {
    assert.ok(
      splitTimestamps(s).every(p => typeof p === 'string'),
      s,
    );
  }
  // Punctuation around a time is fine.
  assert.deepEqual(splitTimestamps('(5:00)'), ['(', {text: '5:00', seconds: 300}, ')']);
  assert.deepEqual(splitTimestamps('5:00,6:00'), [{text: '5:00', seconds: 300}, ',', {text: '6:00', seconds: 360}]);
});

test('splitTimestamps drops times past the duration', () => {
  assert.deepEqual(splitTimestamps('at 59:00 and 2:00:00', 3600), ['at ', {text: '59:00', seconds: 3540}, ' and 2:00:00']);
  assert.deepEqual(splitTimestamps('2:00:00', 0), [{text: '2:00:00', seconds: 7200}]); // unknown duration
});

test('splitTimestamps leaves markup as plain text', () => {
  assert.deepEqual(splitTimestamps('<b>1:00</b>'), ['<b>', {text: '1:00', seconds: 60}, '</b>']);
});
