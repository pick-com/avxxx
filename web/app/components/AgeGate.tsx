import {useId, useState, type FormEvent, type ReactNode} from 'react';
import {ApiError} from '../lib/api';
import {answerAgeGate} from '../lib/ageGate';
import {useT} from '../lib/i18n';
import {Link} from '../lib/nav';
import {Icon} from './Icon';

// Site-entry 18+ gate (PRD 15, AgeGate artboard; DESIGN_ALIGNMENT #8).
//
// The site is adults-only. root.tsx renders <SiteAgeGate> instead of the app
// until the visitor's signed gate cookie says ok, and <AgeRestricted> while it
// says denied (24 hours, no new answer possible). The answer is checked and
// the cookie set by POST /api/age-gate (internal/api/agegate.go); the page
// then reloads so the server renders it with its data. Registration and the
// one-time prompt for older accounts reuse <BirthMonthPicker>.

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

function GateLinks() {
  const t = useT();
  return (
    <nav className="age-gate-links" aria-label={t('age.linksLabel')}>
      <Link to="/legal/terms">{t('age.linkTerms')}</Link>
      <Link to="/legal/privacy">{t('age.linkPrivacy')}</Link>
      <Link to="/help">{t('age.linkHelp')}</Link>
    </nav>
  );
}

function GateFrame({children, labelledBy}: {children: ReactNode; labelledBy: string}) {
  return (
    <main className="age-gate-page">
      <section className="age-gate" aria-labelledby={labelledBy}>
        <span className="logo logo-lg">
          REEL<span className="logo-dot">.</span>
        </span>
        {children}
      </section>
    </main>
  );
}

/** The interstitial shown before any page until the visitor confirms 18+. */
export function SiteAgeGate() {
  const t = useT();
  const [year, setYear] = useState('');
  const [month, setMonth] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!year || !month) {
      setError(t('login.pickBirth'));
      return;
    }
    setBusy(true);
    setError('');
    try {
      await answerAgeGate(Number(year), Number(month));
      // ok: the server renders the page; denied: the restricted page.
      window.location.reload();
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 429 ? t('login.tooMany') : t('age.gateFailed'));
      setBusy(false);
    }
  };

  return (
    <GateFrame labelledBy="age-gate-title">
      <span className="age-badge">18+</span>
      <h1 id="age-gate-title">{t('age.gateTitle')}</h1>
      <p>{t('age.gateBody')}</p>
      <form className="age-gate-form" onSubmit={submit}>
        <BirthMonthPicker year={year} month={month} onYear={setYear} onMonth={setMonth} idPrefix="gate-birth" />
        <p className="muted">
          <Icon name="lock" size={14} /> {t('age.gatePrivacy')}
        </p>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" className="btn btn-primary btn-block btn-lg" disabled={busy || !year || !month}>
          {busy ? t('login.wait') : t('age.gateEnter')}
        </button>
      </form>
      <p className="muted">{t('age.gateNotice')}</p>
      <GateLinks />
    </GateFrame>
  );
}

/** Shown for 24 hours after an under-18 answer: an explanation and the legal pages, nothing else. */
export function AgeRestricted({until}: {until?: string}) {
  const t = useT();
  return (
    <GateFrame labelledBy="age-restricted-title">
      <h1 id="age-restricted-title">{t('age.deniedTitle')}</h1>
      <p>{t('age.deniedBody')}</p>
      <p className="muted">
        <Icon name="lock" size={14} /> {t('age.lockNote')}
        {until && (
          <>
            {' '}
            <time dateTime={until}>{t('age.lockUntil', {time: until.slice(0, 16).replace('T', ' ') + ' UTC'})}</time>
          </>
        )}
      </p>
      <GateLinks />
    </GateFrame>
  );
}
