import assert from 'node:assert/strict';
import test from 'node:test';
import {needsFullscreenExit, overlayHost} from '../app/lib/fullscreenHost.ts';

test('overlayHost: the fullscreen element when it can hold the sheet', () => {
  const shell = {tagName: 'DIV'};
  assert.equal(overlayHost(shell), shell);
  assert.equal(overlayHost(null), null);
  assert.equal(overlayHost(undefined), null);
  for (const tagName of ['VIDEO', 'video', 'IFRAME', 'CANVAS', 'IMG']) assert.equal(overlayHost({tagName}), null, tagName);
});

test('needsFullscreenExit: only a fullscreen element that cannot hold children', () => {
  assert.equal(needsFullscreenExit(null), false);
  assert.equal(needsFullscreenExit({tagName: 'DIV'}), false);
  assert.equal(needsFullscreenExit({tagName: 'VIDEO'}), true);
});
