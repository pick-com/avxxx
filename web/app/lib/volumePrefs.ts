// Player volume and mute, remembered in this browser (not a synced setting).

const KEY = 'player_volume_v1';

export type VolumePref = {volume: number; muted: boolean};

export function readVolume(): VolumePref | null {
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<VolumePref>;
    if (typeof v.volume !== 'number' || !Number.isFinite(v.volume)) return null;
    return {volume: Math.min(1, Math.max(0, v.volume)), muted: v.muted === true};
  } catch {
    return null;
  }
}

export function writeVolume(pref: VolumePref) {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(pref));
  } catch {
    /* not persisted */
  }
}

/** Applies the remembered volume to a new <video>. */
export function applyVolume(video: HTMLVideoElement) {
  const pref = readVolume();
  if (!pref) return;
  video.volume = pref.volume;
  video.muted = pref.muted;
}
