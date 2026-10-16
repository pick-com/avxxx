import {useEffect, useSyncExternalStore} from 'react';
import {getSettings, patchSettings} from './api';
import {track} from './analytics';

// 个人屏蔽词 (PRD 5.5 设置项 · 屏蔽词): danmaku containing one of the viewer's
// words are hidden in this player only. Visitors keep the list in this
// browser; a logged-in user's list lives in user_settings.danmaku_block_words
// (migration 045, PATCH /api/me/settings) and this browser keeps a copy.
// On the first sync after login a server list wins; an empty server list
// takes the words collected while logged out.

/** store.MaxDanmakuBlockWords / MaxDanmakuBlockWordRunes. */
export const MAX_BLOCK_WORDS = 100;
export const MAX_BLOCK_WORD_LENGTH = 20;

const KEY = 'danmaku_block_words_v1';
let current: string[] | null = null;
/** Logged-in user whose list is synced, or null (visitor / not synced yet). */
let syncedUser: number | null = null;
const listeners = new Set<() => void>();
const EMPTY: string[] = [];

function read(): string[] {
  if (current) return current;
  try {
    const raw = window.localStorage.getItem(KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    current = Array.isArray(parsed) ? parsed.filter((w): w is string => typeof w === 'string' && w.trim() !== '').slice(0, MAX_BLOCK_WORDS) : [];
  } catch {
    current = [];
  }
  return current;
}

function write(words: string[]) {
  current = words;
  try {
    window.localStorage.setItem(KEY, JSON.stringify(words));
  } catch {
    /* this page only */
  }
  listeners.forEach(fn => fn());
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function useDanmakuBlockWords(): string[] {
  return useSyncExternalStore(subscribe, read, () => EMPTY);
}

/** Folds text for matching: NFKC, lower case, no whitespace ("Ｓ ｐ" ~ "sp"). */
export const foldBlockText = (s: string) => s.normalize('NFKC').toLowerCase().replace(/\s+/g, '');

/** Whether body contains one of the (unfolded) words. */
export function hitsBlockWord(body: string, folded: string[]): boolean {
  if (folded.length === 0) return false;
  const text = foldBlockText(body);
  return folded.some(w => w !== '' && text.includes(w));
}

const sameWord = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

function save(words: string[]) {
  write(words);
  if (syncedUser !== null) void patchSettings({danmaku_block_words: words}).catch(() => {});
}

/** Adds a word; returns false when it is empty, too long, a duplicate or the list is full. */
export function addBlockWord(raw: string): boolean {
  const word = raw.trim();
  const words = read();
  if (!word || Array.from(word).length > MAX_BLOCK_WORD_LENGTH || words.length >= MAX_BLOCK_WORDS || words.some(w => sameWord(w, word))) return false;
  save([...words, word]);
  track('danmaku_setting', {setting: 'block_words'});
  return true;
}

export function removeBlockWord(word: string) {
  const words = read();
  if (!words.includes(word)) return;
  save(words.filter(w => w !== word));
  track('danmaku_setting', {setting: 'block_words'});
}

/**
 * Syncs the list with the account while a user is logged in (userId), once
 * per user and page load. Logging out keeps this browser's copy.
 */
export function useDanmakuBlockWordsSync(userId: number | null) {
  useEffect(() => {
    if (userId === null) {
      syncedUser = null;
      return;
    }
    if (syncedUser === userId) return;
    const controller = new AbortController();
    getSettings(controller.signal).then(
      s => {
        const server = s.danmaku_block_words;
        if (!server) return; // older backend without the column: stay local
        syncedUser = userId;
        const local = read();
        if (server.length === 0 && local.length > 0) save(local);
        else write(server);
      },
      () => {},
    );
    return () => controller.abort();
  }, [userId]);
}
