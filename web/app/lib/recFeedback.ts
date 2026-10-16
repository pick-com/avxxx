// 推荐反馈 client (internal/api/rec_feedback.go; migration 051):
//
//   POST   /api/recs/feedback {kind, target}     「不感兴趣」 / 「不推荐此演员」 / 「不推荐此类型」
//   DELETE /api/recs/feedback?kind=&target=      撤销
//   GET    /api/me/rec-feedback                  Settings › 隐私 › 推荐反馈
//
// Signed-in viewers are identified by their token; visitors by the same
// X-Device-ID as ratings and 赞 / 踩. A visitor's GET /api/recs only sends
// the device id once this browser has given feedback (recsDeviceHeaders), so
// plain anonymous recommendations stay cacheable.

import {api, tokens} from './api';
import {ratingDeviceId} from './community';

export type RecFeedbackKind = 'not_interested_release' | 'not_interested_performer' | 'not_interested_tag';

/** store.RecFeedback: target is the release / performer slug or the tag; name its title, name or the tag. */
export type RecFeedback = {kind: RecFeedbackKind; target: string; name: string; created_at: string};

const USED_KEY = 'rec_feedback_device';
let deviceUsed = false;

function markDeviceUsed() {
  deviceUsed = true; // without storage (private mode): this page load only
  try {
    window.localStorage.setItem(USED_KEY, '1');
  } catch {
    // ignore
  }
}

function deviceHasFeedback(): boolean {
  if (typeof window === 'undefined') return false;
  if (deviceUsed) return true;
  try {
    return window.localStorage.getItem(USED_KEY) === '1';
  } catch {
    return false;
  }
}

const feedbackHeaders = (): Record<string, string> | undefined => (tokens.user() ? undefined : {'X-Device-ID': ratingDeviceId()});

/** Headers for GET /api/recs: the visitor's device id once it has given feedback. */
export function recsDeviceHeaders(): Record<string, string> | undefined {
  return !tokens.user() && deviceHasFeedback() ? {'X-Device-ID': ratingDeviceId()} : undefined;
}

export async function addRecFeedback(kind: RecFeedbackKind, target: string): Promise<RecFeedback> {
  const headers = feedbackHeaders();
  const out = await api<RecFeedback>('recs/feedback', {method: 'POST', body: {kind, target}, headers});
  if (headers) markDeviceUsed();
  return out;
}

export const undoRecFeedback = (kind: RecFeedbackKind, target: string) =>
  api<{undone: boolean}>(`recs/feedback?kind=${encodeURIComponent(kind)}&target=${encodeURIComponent(target)}`, {method: 'DELETE', headers: feedbackHeaders()});

export const listRecFeedback = (signal?: AbortSignal) =>
  api<{items: RecFeedback[] | null}>('me/rec-feedback', {signal, headers: feedbackHeaders()}).then(r => r.items ?? []);
