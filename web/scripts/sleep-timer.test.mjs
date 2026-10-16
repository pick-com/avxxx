// 定时停止 math (app/lib/sleepTimer.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {FADE_MS, SLEEP_OFF, fadeFactor, sleepOnNavigate, sleepRemaining, sleepSecondsLeft, sleepTick, startSleep} from '../app/lib/sleepTimer.ts';

const T0 = 1_000_000;

test('timed choices set a wall-clock deadline', () => {
  assert.deepEqual(startSleep(15, T0), {choice: 15, deadline: T0 + 15 * 60_000});
  assert.deepEqual(startSleep(60, T0), {choice: 60, deadline: T0 + 3_600_000});
  assert.deepEqual(startSleep('episode', T0), {choice: 'episode', deadline: null});
  assert.deepEqual(startSleep('off', T0), SLEEP_OFF);
});

test('remaining time never goes negative', () => {
  const s = startSleep(30, T0);
  assert.equal(sleepRemaining(s, T0), 30 * 60_000);
  assert.equal(sleepRemaining(s, T0 + 31 * 60_000), 0);
  assert.equal(sleepRemaining(startSleep('episode', T0), T0), null);
  assert.equal(sleepRemaining(SLEEP_OFF, T0), null);
});

test('the fade runs over the last 10 seconds', () => {
  assert.equal(FADE_MS, 10_000);
  assert.equal(fadeFactor(60_000), 1);
  assert.equal(fadeFactor(10_000), 1);
  assert.equal(fadeFactor(5_000), 0.5);
  assert.equal(fadeFactor(0), 0);
  assert.equal(fadeFactor(-5), 0);
  assert.equal(fadeFactor(NaN), 1);
});

test('ticks: wait, fade, fire', () => {
  const s = startSleep(15, T0);
  const end = s.deadline;
  assert.deepEqual(sleepTick(SLEEP_OFF, T0), {phase: 'idle', factor: 1});
  assert.deepEqual(sleepTick(startSleep('episode', T0), T0), {phase: 'idle', factor: 1});
  assert.deepEqual(sleepTick(s, T0), {phase: 'wait', factor: 1});
  assert.deepEqual(sleepTick(s, end - 2_500), {phase: 'fade', factor: 0.25});
  assert.deepEqual(sleepTick(s, end), {phase: 'fire', factor: 0});
  assert.deepEqual(sleepTick(s, end + 60_000), {phase: 'fire', factor: 0});
});

test('another release ends a timed choice but keeps 播完本集', () => {
  assert.deepEqual(sleepOnNavigate(startSleep(45, T0)), SLEEP_OFF);
  assert.deepEqual(sleepOnNavigate(startSleep('episode', T0)), {choice: 'episode', deadline: null});
  assert.deepEqual(sleepOnNavigate(SLEEP_OFF), SLEEP_OFF);
});

test('seconds left round up for the menu', () => {
  const s = startSleep(15, T0);
  assert.equal(sleepSecondsLeft(s, T0), 900);
  assert.equal(sleepSecondsLeft(s, s.deadline - 1), 1);
  assert.equal(sleepSecondsLeft(s, s.deadline), 0);
  assert.equal(sleepSecondsLeft(SLEEP_OFF, T0), null);
});
