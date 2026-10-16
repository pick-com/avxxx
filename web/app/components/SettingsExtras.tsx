import {useEffect, useState} from 'react';
import {clearSearchHistory, getPlaybackProviders, type PlaybackProvider, type UserSettings} from '../lib/api';
import {useT} from '../lib/i18n';
import {clearRecentSearches} from '../lib/recentSearches';
import {useSettings} from '../lib/settings';

// Two Settings rows kept out of routes/settings.tsx:
//   默认片源 (播放): preferred_provider on PATCH /api/me/settings; play-config
//     orders that provider's sources first for this user (within main /
//     alternate and active / degraded groups). Options are the providers
//     with playable sources (GET /api/playback/providers); a stored provider
//     that no longer has sources stays selectable so the value is visible.
//   清除搜索记录 (隐私): DELETE /api/me/search-history plus this browser's list.

/** Settings › 播放 › 默认片源. Hidden on a backend without the field. */
export function DefaultSourceRow({s}: {s: UserSettings}) {
  const t = useT();
  const {save, fieldErrors} = useSettings();
  const [providers, setProviders] = useState<PlaybackProvider[] | null>(null);
  useEffect(() => {
    const ctrl = new AbortController();
    getPlaybackProviders(ctrl.signal)
      .then(setProviders)
      .catch(() => {
        if (!ctrl.signal.aborted) setProviders([]);
      });
    return () => ctrl.abort();
  }, []);
  if (s.preferred_provider === undefined) return null;
  const value = s.preferred_provider ?? '';
  const names = (providers ?? []).map(p => p.provider);
  if (value && !names.includes(value)) names.push(value);
  return (
    <div className="set-row">
      <div className="set-text">
        <label className="set-label" htmlFor="set-default-source">
          {t('settings.defaultSource')}
        </label>
        <span className="set-desc">{t('settings.defaultSourceDesc')}</span>
        {fieldErrors.preferred_provider && (
          <span className="field-error" role="alert">
            {fieldErrors.preferred_provider}
          </span>
        )}
      </div>
      <select
        id="set-default-source"
        className="set-select"
        value={value}
        onChange={e => {
          const v = e.target.value;
          void save('preferred_provider', {preferred_provider: v}, cur => ({...cur, preferred_provider: v}));
        }}
      >
        <option value="">{t('settings.defaultSourceAuto')}</option>
        {names.map(n => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </div>
  );
}

/** Settings › 隐私 › 清除搜索记录 (account history and this browser's list). */
export function ClearSearchHistoryRow() {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState('');
  const [error, setError] = useState('');
  const clear = async () => {
    setBusy(true);
    setDone('');
    setError('');
    clearRecentSearches();
    try {
      const r = await clearSearchHistory();
      setDone(t('settings.searchHistoryClearedN', {n: r?.deleted ?? 0}));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('settings.clearFailed'));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="set-row">
      <div className="set-text">
        <span className="set-label">{t('settings.clearSearchHistory')}</span>
        <span className="set-desc">{t('settings.clearSearchHistoryDesc')}</span>
        <span className="sr-status" role="status">
          {done}
        </span>
        {error && (
          <span className="field-error" role="alert">
            {error}
          </span>
        )}
        {done && <span className="set-desc">{done}</span>}
      </div>
      <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => void clear()}>
        {busy ? t('settings.clearing') : t('settings.clear')}
      </button>
    </div>
  );
}
