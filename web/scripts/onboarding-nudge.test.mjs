import assert from 'node:assert/strict';
import test from 'node:test';
import {onboardingNudge, welcomeHref} from '../app/lib/onboardingNudge.ts';

test('onboardingNudge: only a new account that has not finished onboarding', () => {
  assert.equal(onboardingNudge(true, {onboarded: false}), 'show');
  assert.equal(onboardingNudge(true, {onboarded: true}), 'drop');
  assert.equal(onboardingNudge(false, {onboarded: false}), 'drop');
});

test('onboardingNudge: waits for the full profile', () => {
  assert.equal(onboardingNudge(true, null), 'wait');
  // The sign-up response (and an older backend) has no `onboarded`.
  assert.equal(onboardingNudge(true, {}), 'wait');
});

test('welcomeHref: returns to the page, never off-site', () => {
  assert.equal(welcomeHref('/releases/abc-123?t=30'), '/welcome?next=%2Freleases%2Fabc-123%3Ft%3D30');
  assert.equal(welcomeHref('//evil.example'), '/welcome?next=%2F');
  assert.equal(welcomeHref('https://evil.example'), '/welcome?next=%2F');
});
