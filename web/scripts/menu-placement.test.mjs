import assert from 'node:assert/strict';
import test from 'node:test';
import {offscreen, placeMenu, visibleArea} from '../app/lib/menuPlacement.ts';

const viewport = {width: 1000, height: 800};
const menu = {width: 200, height: 150};
const rect = (left, top, w = 32, h = 32) => ({left, top, right: left + w, bottom: top + h});

test('placeMenu: below the trigger, start-aligned', () => {
  assert.deepEqual(placeMenu(rect(100, 100), menu, viewport), {top: 136, left: 100, side: 'below'});
});

test('placeMenu: end-aligned for rows', () => {
  assert.deepEqual(placeMenu(rect(500, 100), menu, viewport, 'end'), {top: 136, left: 332, side: 'below'});
});

test('placeMenu: flips above when there is no room below', () => {
  const p = placeMenu(rect(100, 700), menu, viewport);
  assert.equal(p.side, 'above');
  assert.equal(p.top, 700 - 4 - 150);
  assert.equal(p.maxHeight, undefined);
});

test('placeMenu: shifted inside the right and left edges', () => {
  assert.equal(placeMenu(rect(950, 100), menu, viewport).left, 1000 - 8 - 200);
  assert.equal(placeMenu(rect(20, 100), menu, viewport, 'end').left, 8);
  assert.equal(placeMenu(rect(-20, 100), menu, viewport).left, 8);
});

test('placeMenu: too tall for either side takes the taller one and scrolls', () => {
  const tall = {width: 200, height: 700};
  const p = placeMenu(rect(100, 300), tall, viewport);
  assert.equal(p.side, 'below');
  assert.equal(p.top, 336);
  assert.equal(p.maxHeight, 800 - 8 - 336);
  const q = placeMenu(rect(100, 500), tall, viewport);
  assert.equal(q.side, 'above');
  assert.equal(q.maxHeight, 500 - 4 - 8);
  assert.equal(q.top, 8);
});

test('offscreen: only once the trigger has left the visible area', () => {
  const area = visibleArea(viewport, []);
  assert.equal(offscreen(rect(100, 100), area), false);
  assert.equal(offscreen(rect(990, 100), area), false);
  assert.equal(offscreen(rect(-40, 100), area), true);
  assert.equal(offscreen(rect(1000, 100), area), true);
  assert.equal(offscreen(rect(100, -40), area), true);
});

test('visibleArea: a scrolling shelf narrows it; offscreen follows the shelf', () => {
  const shelf = {left: 250, top: 200, right: 900, bottom: 450};
  const area = visibleArea(viewport, [shelf, {left: 0, top: -500, right: 1300, bottom: 2000}]);
  assert.deepEqual(area, {left: 250, top: 200, right: 900, bottom: 450});
  // Scrolled out of the shelf to the left, though still inside the viewport.
  assert.equal(offscreen(rect(200, 300), area), true);
  assert.equal(offscreen(rect(240, 300), area), false);
});
