import {useId} from 'react';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';
import {abActive, type AbLoop} from '../lib/playerLoop';
import {SLEEP_CHOICES, type SleepChoice} from '../lib/sleepTimer';
import {SwitchRow} from './Switch';

/**
 * 播放 tab of the player settings sheet (desktop gear and mobile sheet):
 * 循环播放, the A-B loop, 定时停止 and 氛围模式. The Player owns the state
 * and applies it through the <video> API; this only renders and reports.
 */
export function PlayerPlaybackPanel({loop, ab, sleep, ambient}: {
  loop: {on: boolean; onChange: (on: boolean) => void};
  /** 氛围模式; `available` false: the source's frames cannot be read (toggle hidden, hint shown). */
  ambient: {on: boolean; available: boolean; onChange: (on: boolean) => void};
  /** A / B from the current time; `time` labels the buttons. */
  ab: {value: AbLoop; time: number; setA: () => void; setB: () => void; clear: () => void};
  /** 定时停止; `left`: whole seconds of a running timed choice. */
  sleep: {value: SleepChoice; left: number | null; onChange: (choice: SleepChoice) => void};
}) {
  const t = useT();
  const sleepLabel = (c: SleepChoice) => (c === 'off' ? t('player.off') : c === 'episode' ? t('player.sleepEpisode') : t('player.sleepMinutes', {n: c}));
  const sleepStatus =
    sleep.left !== null ? t('player.sleepLeft', {time: formatClock(sleep.left)}) : sleep.value === 'episode' ? t('player.sleepEpisodeSet') : t('player.sleepHint');
  const sleepId = useId();
  const {a, b} = ab.value;
  const status = abActive(ab.value)
    ? t('player.abActive', {a: formatClock(a!), b: formatClock(b!)})
    : a !== null
      ? t('player.abOnlyA', {a: formatClock(a)})
      : t('player.abHint');
  return (
    <div className="pp-panel">
      <SwitchRow label={t('player.loop')} description={t('player.loopHint')} checked={loop.on} onChange={loop.onChange} />
      <div className="set-row set-row-stack" role="group" aria-label={t('player.abLoop')}>
        <div className="set-text">
          <span className="set-label">{t('player.abLoop')}</span>
          <span className="set-desc" aria-live="polite">
            {status}
          </span>
        </div>
        <div className="pp-actions">
          <button type="button" className="btn btn-secondary btn-sm" onClick={ab.setA}>
            {t('player.abSetA', {time: formatClock(ab.time)})}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" onClick={ab.setB}>
            {t('player.abSetB', {time: formatClock(ab.time)})}
          </button>
          {(a !== null || b !== null) && (
            <button type="button" className="btn btn-outline btn-sm" onClick={ab.clear}>
              {t('player.abClear')}
            </button>
          )}
        </div>
      </div>
      <div className="set-row set-row-stack">
        <div className="set-text">
          <span className="set-label" id={sleepId}>
            {t('player.sleep')}
          </span>
          <span className="set-desc">{sleepStatus}</span>
        </div>
        <div className="pp-actions" role="radiogroup" aria-labelledby={sleepId}>
          {SLEEP_CHOICES.map(c => (
            <button
              key={String(c)}
              type="button"
              role="radio"
              aria-checked={c === sleep.value}
              className={c === sleep.value ? 'chip chip-on' : 'chip'}
              onClick={() => sleep.onChange(c)}
            >
              {sleepLabel(c)}
            </button>
          ))}
        </div>
      </div>
      {ambient.available ? (
        <SwitchRow label={t('player.ambient')} description={t('player.ambientHint')} checked={ambient.on} onChange={ambient.onChange} />
      ) : (
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('player.ambient')}</span>
            <span className="set-desc">{t('player.ambientBlocked')}</span>
          </div>
        </div>
      )}
    </div>
  );
}
