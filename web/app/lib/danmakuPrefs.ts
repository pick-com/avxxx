import {useSyncExternalStore} from 'react';
import {track} from './analytics';

// Danmaku display settings (PRD 5.5 设置项, 5.5.1 显示高能波峰). Kept in this
// browser: the settings API has no danmaku fields yet.

export type DanmakuSpeed = 'slow' | 'normal' | 'fast';
export type DanmakuSize = 'small' | 'normal' | 'large';
export type DanmakuArea = 0.25 | 0.5 | 1;

export type DanmakuPrefs = {
  enabled: boolean;
  /** 0.2–1 */
  opacity: number;
  size: DanmakuSize;
  speed: DanmakuSpeed;
  /** 显示区域: 1/4, 半屏, 满屏 */
  area: DanmakuArea;
  blockTop: boolean;
  blockBottom: boolean;
  blockScroll: boolean;
  /** 彩色: anything not white */
  blockColor: boolean;
  /** 重复: the same text within a few seconds */
  blockRepeat: boolean;
  /** 剧透: danmaku folded as spoilers (spoiler:true); also hides the top-3 peak markers on a first watch */
  blockSpoiler: boolean;
  /** 防挡字幕: keep the bottom 15 % free */
  antiSubtitle: boolean;
  /** 显示高能波峰 */
  showPeaks: boolean;
  /** 发送样式 */
  sendMode: 'scroll' | 'top' | 'bottom';
  sendColor: string;
};

/** Send colors (#RRGGBB, community.ValidColor). */
export const DANMAKU_COLORS = ['#FFFFFF', '#FE0302', '#FFAA02', '#FFE133', '#00CD00', '#00A1D6', '#CC0273'] as const;

export const DEFAULT_DANMAKU_PREFS: DanmakuPrefs = {
  enabled: true,
  opacity: 0.85,
  size: 'normal',
  speed: 'normal',
  area: 0.5,
  blockTop: false,
  blockBottom: false,
  blockScroll: false,
  blockColor: false,
  blockRepeat: false,
  blockSpoiler: true,
  antiSubtitle: true,
  showPeaks: true,
  sendMode: 'scroll',
  sendColor: '#FFFFFF',
};

const KEY = 'danmaku_prefs_v1';
let current: DanmakuPrefs | null = null;
const listeners = new Set<() => void>();

function read(): DanmakuPrefs {
  if (current) return current;
  let stored: Partial<DanmakuPrefs> = {};
  try {
    const raw = window.localStorage.getItem(KEY);
    stored = raw ? (JSON.parse(raw) as Partial<DanmakuPrefs>) : {};
  } catch {
    stored = {};
  }
  const d = DEFAULT_DANMAKU_PREFS;
  const bool = (k: keyof DanmakuPrefs) => (typeof stored[k] === 'boolean' ? (stored[k] as boolean) : (d[k] as boolean));
  current = {
    enabled: bool('enabled'),
    opacity: typeof stored.opacity === 'number' && stored.opacity >= 0.2 && stored.opacity <= 1 ? stored.opacity : d.opacity,
    size: stored.size === 'small' || stored.size === 'large' || stored.size === 'normal' ? stored.size : d.size,
    speed: stored.speed === 'slow' || stored.speed === 'fast' || stored.speed === 'normal' ? stored.speed : d.speed,
    area: stored.area === 0.25 || stored.area === 1 || stored.area === 0.5 ? stored.area : d.area,
    blockTop: bool('blockTop'),
    blockBottom: bool('blockBottom'),
    blockScroll: bool('blockScroll'),
    blockColor: bool('blockColor'),
    blockRepeat: bool('blockRepeat'),
    blockSpoiler: bool('blockSpoiler'),
    antiSubtitle: bool('antiSubtitle'),
    showPeaks: bool('showPeaks'),
    sendMode: stored.sendMode === 'top' || stored.sendMode === 'bottom' ? stored.sendMode : 'scroll',
    sendColor: (DANMAKU_COLORS as readonly string[]).includes(stored.sendColor ?? '') ? (stored.sendColor as string) : d.sendColor,
  };
  return current;
}

const SETTING_NAMES: Record<keyof DanmakuPrefs, string> = {
  enabled: 'enabled',
  opacity: 'opacity',
  size: 'size',
  speed: 'speed',
  area: 'area',
  blockTop: 'block_top',
  blockBottom: 'block_bottom',
  blockScroll: 'block_scroll',
  blockColor: 'block_color',
  blockRepeat: 'block_repeat',
  blockSpoiler: 'block_spoiler',
  antiSubtitle: 'anti_subtitle',
  showPeaks: 'show_peaks',
  sendMode: 'send_mode',
  sendColor: 'send_color',
};

/** Change one setting; reports danmaku_toggle / danmaku_setting. */
export function setDanmakuPref<K extends keyof DanmakuPrefs>(key: K, value: DanmakuPrefs[K]) {
  const prev = read();
  if (prev[key] === value) return;
  current = {...prev, [key]: value};
  try {
    window.localStorage.setItem(KEY, JSON.stringify(current));
  } catch {
    /* this page only */
  }
  listeners.forEach(fn => fn());
  if (key === 'enabled') track('danmaku_toggle', {enabled: value as boolean});
  else track('danmaku_setting', {setting: SETTING_NAMES[key]});
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function useDanmakuPrefs(): DanmakuPrefs {
  return useSyncExternalStore(subscribe, read, () => DEFAULT_DANMAKU_PREFS);
}

export const FONT_PX: Record<DanmakuSize, number> = {small: 18, normal: 22, large: 28};
/** Scroll speed in px per second at 1280 px width (scaled with the player width). */
export const SPEED_PX: Record<DanmakuSpeed, number> = {slow: 110, normal: 160, fast: 230};
