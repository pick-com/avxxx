import {useEffect, useId, useState} from 'react';
import {useT} from '../lib/i18n';
import {Icon} from './Icon';

// Age confirmation (PRD 15, AgeGate artboard).
//
// Registration asks for birth year and month and blocks under-13s
// (login.tsx). AgeConfirm is the per-video 18+ screen; it is not mounted
// anywhere yet because releases carry no age rating in the API.
// TODO(PRD 15 / age_verifications): mount <AgeConfirm> on the video page for
// releases with an age rating and record the confirmation server-side.
// Teen mode (time limits, parent password) is out of scope: positioning is undecided.

export const MIN_REGISTER_AGE = 13;
const LOCK_KEY = 'age_gate_v1';
const LOCK_MS = 24 * 60 * 60 * 1000;

/**
 * Whole years at `now` from a birth year and month. The day is unknown, so the
 * birth month itself counts as not yet reached (conservative).
 */
export function ageFromYearMonth(year: number, month: number, now = new Date()): number {
  let age = now.getFullYear() - year;
  if (now.getMonth() + 1 <= month) age -= 1;
  return age;
}

export function BirthMonthPicker({year, month, onYear, onMonth, idPrefix}: {
  year: string;
  month: string;
  onYear: (v: string) => void;
  onMonth: (v: string) => void;
  idPrefix?: string;
}) {
  const t = useT();
  const auto = useId();
  const id = idPrefix ?? auto;
  const thisYear = new Date().getFullYear();
  const years = Array.from({length: 100}, (_, i) => thisYear - i);
  return (
    <div className="birth-picker">
      <label className="field" htmlFor={`${id}-y`}>
        <span>{t('age.birthYear')}</span>
        <select id={`${id}-y`} className="set-select" value={year} onChange={e => onYear(e.target.value)} required>
          <option value="">{t('age.yearPlaceholder')}</option>
          {years.map(y => (
            <option key={y} value={y}>
              {t('common.year', {y})}
            </option>
          ))}
        </select>
      </label>
      <label className="field" htmlFor={`${id}-m`}>
        <span>{t('age.birthMonth')}</span>
        <select id={`${id}-m`} className="set-select" value={month} onChange={e => onMonth(e.target.value)} required>
          <option value="">{t('age.monthPlaceholder')}</option>
          {Array.from({length: 12}, (_, i) => i + 1).map(m => (
            <option key={m} value={m}>
              {t('age.month', {m})}
            </option>
          ))}
        </select>
      </label>
    </div>
  );
}

type Lock = {year: number; month: number; at: number};

function readLock(): Lock | null {
  try {
    const v = JSON.parse(window.localStorage.getItem(LOCK_KEY) ?? 'null') as Lock | null;
    return v && Date.now() - v.at < LOCK_MS ? v : null;
  } catch {
    return null;
  }
}

/** Per-video confirmation for rated releases (unused until the API exposes a rating). */
export function AgeConfirm({minAge = 18, reason, onConfirm, onBack}: {minAge?: number; reason?: string; onConfirm: () => void; onBack: () => void}) {
  const t = useT();
  const [year, setYear] = useState('');
  const [month, setMonth] = useState('');
  const [blocked, setBlocked] = useState(false);
  const [lock, setLock] = useState<Lock | null>(null);

  useEffect(() => {
    const l = readLock();
    setLock(l);
    if (l) {
      setYear(String(l.year));
      setMonth(String(l.month));
      if (ageFromYearMonth(l.year, l.month) < minAge) setBlocked(true);
    }
  }, [minAge]);

  const confirm = () => {
    const y = Number(year);
    const m = Number(month);
    if (!y || !m) return;
    if (!lock) {
      const next = {year: y, month: m, at: Date.now()};
      try {
        window.localStorage.setItem(LOCK_KEY, JSON.stringify(next));
      } catch {
        /* not persisted */
      }
      setLock(next);
    }
    if (ageFromYearMonth(y, m) >= minAge) onConfirm();
    else setBlocked(true);
  };

  if (blocked) {
    return (
      <section className="age-gate" role="alert">
        <h2>{t('age.blockedTitle')}</h2>
        <p>{t('age.blockedBody')}</p>
        <p className="muted">{t('age.lockNote')}</p>
        <button type="button" className="btn btn-secondary" onClick={onBack}>
          {t('common.back')}
        </button>
      </section>
    );
  }

  return (
    <section className="age-gate" aria-labelledby="age-gate-title">
      <span className="age-badge">{minAge}+</span>
      <h2 id="age-gate-title">{t('age.title', {age: minAge})}</h2>
      <p>
        {reason ? t('age.reason', {reason}) : ''}
        {t('age.prompt')}
      </p>
      <BirthMonthPicker year={year} month={month} onYear={lock ? () => {} : setYear} onMonth={lock ? () => {} : setMonth} />
      {lock && (
        <p className="muted">
          <Icon name="lock" size={14} /> {t('age.lockNote')}
        </p>
      )}
      <div className="action-row">
        <button type="button" className="btn btn-secondary" onClick={onBack}>
          {t('common.back')}
        </button>
        <button type="button" className="btn btn-primary" disabled={!year || !month} onClick={confirm}>
          {t('age.confirm')}
        </button>
      </div>
    </section>
  );
}
