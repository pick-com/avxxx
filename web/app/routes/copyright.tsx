import {useEffect, useId, useState, type FormEvent, type ReactNode} from 'react';
import {useSearchParams, type MetaFunction} from 'react-router';
import {Link} from '../lib/nav';
import {ApiError} from '../lib/api';
import {getTakedownStatus, postCounterNotice, postTakedown, type TakedownPublicStatus, type TakedownReceipt} from '../lib/compliance';
import {formatDate} from '../lib/format';
import {t, useI18n, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {seoMeta} from '../lib/seo';
import '../styles/requests.css';
import '../styles/compliance.css';

// CopyrightForm (PRD 15; takedown_requests): a copyright notice, the status
// lookup (ticket + email; the email travels in a header, never the URL) and
// the counter-notice on the same ticket. Filing returns a random ticket
// number; nothing a claimant sends is ever shown publicly. 24 h after filing
// the content is taken down or the notice answered (AdminOps 版权投诉).

export const meta: MetaFunction = ({matches}) => seoMeta({matches, path: '/copyright', title: t('title.copyright'), description: t('copyright.intro')});

const TABS = ['notice', 'lookup', 'counter'] as const;
type Tab = (typeof TABS)[number];
const EMAIL = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;
const lines = (s: string) =>
  s
    .split(/\s+/)
    .map(x => x.trim())
    .filter(Boolean);
const httpURL = (s: string) => /^https?:\/\/\S+$/i.test(s);

const STATUS_KEYS: Record<string, MessageKey> = {
  received: 'copyright.status.received',
  reviewing: 'copyright.status.reviewing',
  actioned: 'copyright.status.actioned',
  rejected: 'copyright.status.rejected',
  counter_received: 'copyright.status.counter_received',
  restored: 'copyright.status.restored',
  closed: 'copyright.status.closed',
};

function errorText(t: ReturnType<typeof useT>, cause: unknown): string {
  if (cause instanceof ApiError && cause.status === 429) return t('copyright.rateLimited');
  if (cause instanceof ApiError && cause.status === 400) return t('copyright.invalid');
  if (cause instanceof ApiError && cause.status === 409) return t('copyright.counterNotAllowed');
  return t('copyright.failed');
}

function Field({id, label, required, hint, children}: {id: string; label: string; required?: boolean; hint?: string; children: ReactNode}) {
  return (
    <div className="form-field">
      <label htmlFor={id}>
        {label} {required && <span className="req-mark">*</span>}
      </label>
      {children}
      {hint && <span className="muted small">{hint}</span>}
    </div>
  );
}

function NoticeForm() {
  const t = useT();
  const {uiLang} = useI18n();
  const uid = useId();
  const id = (s: string) => `${uid}-${s}`;
  const [f, setF] = useState({name: '', email: '', phone: '', address: '', owner: '', relationship: 'owner' as 'owner' | 'agent', work: '', originals: '', urls: '', signature: ''});
  const [goodFaith, setGoodFaith] = useState(false);
  const [accuracy, setAccuracy] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [receipt, setReceipt] = useState<TakedownReceipt | null>(null);
  const set = (k: keyof typeof f) => (e: {target: {value: string}}) => setF(prev => ({...prev, [k]: e.target.value}));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    const urls = lines(f.urls);
    const originals = lines(f.originals);
    if (!f.name.trim() || !f.owner.trim() || !f.work.trim()) return setError(t('copyright.errRequired'));
    if (!EMAIL.test(f.email.trim())) return setError(t('copyright.errEmail'));
    if (urls.length === 0 || urls.length > 20 || !urls.every(httpURL) || !originals.every(httpURL)) return setError(t('copyright.errUrls'));
    if (!goodFaith || !accuracy || !f.signature.trim()) return setError(t('copyright.errStatements'));
    setBusy(true);
    try {
      const r = await postTakedown({
        claimant_name: f.name.trim(),
        claimant_email: f.email.trim(),
        claimant_phone: f.phone.trim(),
        claimant_address: f.address.trim(),
        rights_owner: f.owner.trim(),
        relationship: f.relationship,
        work_description: f.work.trim(),
        original_urls: originals,
        infringing_urls: urls,
        good_faith: goodFaith,
        accuracy,
        signature: f.signature.trim(),
        lang: uiLang,
      });
      setReceipt(r);
    } catch (cause) {
      setError(errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };

  if (receipt) {
    return (
      <section className="perf-req" aria-live="polite">
        <h2>{t('copyright.doneTitle')}</h2>
        <p className="support-ticket">{receipt.ticket}</p>
        <p>{t('copyright.doneBody', {due: formatDate(receipt.due_at)})}</p>
      </section>
    );
  }

  return (
    <section className="perf-req">
      <p className="muted">{t('copyright.noticeIntro')}</p>
      <form className="perf-req-form" onSubmit={e => void submit(e)} noValidate>
        <fieldset className="form-field">
          <legend>{t('copyright.relationship')}</legend>
          <div className="perf-req-kinds">
            {(['owner', 'agent'] as const).map(r => (
              <label key={r} className={`perf-req-kind ${f.relationship === r ? 'is-on' : ''}`}>
                <input type="radio" name="relationship" checked={f.relationship === r} onChange={() => setF(p => ({...p, relationship: r}))} />
                <span>
                  <strong>{t(`copyright.relationship.${r}`)}</strong>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="perf-req-row">
          <Field id={id('name')} label={t('copyright.name')} required>
            <input id={id('name')} className="input" autoComplete="name" maxLength={200} value={f.name} onChange={set('name')} />
          </Field>
          <Field id={id('owner')} label={t('copyright.owner')} required>
            <input id={id('owner')} className="input" autoComplete="organization" maxLength={200} value={f.owner} onChange={set('owner')} />
          </Field>
        </div>
        <div className="perf-req-row">
          <Field id={id('email')} label={t('copyright.email')} required>
            <input id={id('email')} className="input" type="email" autoComplete="email" maxLength={254} value={f.email} onChange={set('email')} />
          </Field>
          <Field id={id('phone')} label={t('copyright.phone')}>
            <input id={id('phone')} className="input" type="tel" autoComplete="tel" maxLength={50} value={f.phone} onChange={set('phone')} />
          </Field>
        </div>
        <Field id={id('address')} label={t('copyright.address')}>
          <input id={id('address')} className="input" autoComplete="street-address" maxLength={500} value={f.address} onChange={set('address')} />
        </Field>
        <Field id={id('work')} label={t('copyright.work')} required hint={t('copyright.workHint')}>
          <textarea id={id('work')} className="input" rows={3} maxLength={4000} value={f.work} onChange={set('work')} />
        </Field>
        <Field id={id('originals')} label={t('copyright.originals')} hint={t('copyright.urlsHint')}>
          <textarea id={id('originals')} className="input" rows={2} value={f.originals} onChange={set('originals')} placeholder="https://" />
        </Field>
        <Field id={id('urls')} label={t('copyright.urls')} required hint={t('copyright.urlsHint')}>
          <textarea id={id('urls')} className="input" rows={3} value={f.urls} onChange={set('urls')} placeholder="https://" />
        </Field>
        <label className="support-check">
          <input type="checkbox" checked={goodFaith} onChange={e => setGoodFaith(e.target.checked)} />
          <span>{t('copyright.goodFaith')}</span>
        </label>
        <label className="support-check">
          <input type="checkbox" checked={accuracy} onChange={e => setAccuracy(e.target.checked)} />
          <span>{t('copyright.accuracy')}</span>
        </label>
        <Field id={id('signature')} label={t('copyright.signature')} required hint={t('copyright.signatureHint')}>
          <input id={id('signature')} className="input" maxLength={200} value={f.signature} onChange={set('signature')} />
        </Field>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" className="btn btn-primary perf-req-submit" disabled={busy}>
          {busy ? t('copyright.submitting') : t('copyright.submitNotice')}
        </button>
      </form>
    </section>
  );
}

function LookupForm({initialTicket, onCounter}: {initialTicket: string; onCounter: (ticket: string) => void}) {
  const t = useT();
  const uid = useId();
  const id = (s: string) => `${uid}-${s}`;
  const [ticket, setTicket] = useState(initialTicket);
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [status, setStatus] = useState<TakedownPublicStatus | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setStatus(null);
    if (!ticket.trim() || !EMAIL.test(email.trim())) return setError(t('copyright.errLookup'));
    setBusy(true);
    try {
      setStatus(await getTakedownStatus(ticket, email));
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 404 ? t('copyright.notFound') : errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="perf-req">
      <p className="muted">{t('copyright.lookupIntro')}</p>
      <form className="perf-req-form" onSubmit={e => void submit(e)} noValidate>
        <div className="perf-req-row">
          <Field id={id('ticket')} label={t('copyright.ticket')} required>
            <input id={id('ticket')} className="input" autoComplete="off" placeholder="TD-XXXX-XXXX-XXXX-XXXX" value={ticket} onChange={e => setTicket(e.target.value)} />
          </Field>
          <Field id={id('email')} label={t('copyright.lookupEmail')} required>
            <input id={id('email')} className="input" type="email" autoComplete="email" value={email} onChange={e => setEmail(e.target.value)} />
          </Field>
        </div>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" className="btn btn-primary perf-req-submit" disabled={busy}>
          {busy ? t('copyright.looking') : t('copyright.lookup')}
        </button>
      </form>
      {status && (
        <div aria-live="polite">
          <p>
            <strong>{t(STATUS_KEYS[status.status] ?? 'copyright.status.received')}</strong>
          </p>
          <p className="muted">{t('copyright.statusDates', {received: formatDate(status.received_at), updated: formatDate(status.updated_at)})}</p>
          {status.counter_allowed && (
            <button type="button" className="btn btn-secondary perf-req-submit" onClick={() => onCounter(status.ticket)}>
              {t('copyright.toCounter')}
            </button>
          )}
        </div>
      )}
    </section>
  );
}

function CounterForm({initialTicket}: {initialTicket: string}) {
  const t = useT();
  const uid = useId();
  const id = (s: string) => `${uid}-${s}`;
  const [f, setF] = useState({ticket: initialTicket, name: '', email: '', address: '', statement: '', signature: ''});
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState<TakedownPublicStatus | null>(null);
  const set = (k: keyof typeof f) => (e: {target: {value: string}}) => setF(prev => ({...prev, [k]: e.target.value}));
  useEffect(() => setF(p => ({...p, ticket: initialTicket || p.ticket})), [initialTicket]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    if (!f.ticket.trim() || !f.name.trim() || !f.address.trim() || !f.statement.trim()) return setError(t('copyright.errRequired'));
    if (!EMAIL.test(f.email.trim())) return setError(t('copyright.errEmail'));
    if (!consent || !f.signature.trim()) return setError(t('copyright.errStatements'));
    setBusy(true);
    try {
      setDone(
        await postCounterNotice(f.ticket, {
          name: f.name.trim(),
          email: f.email.trim(),
          address: f.address.trim(),
          statement: f.statement.trim(),
          consent,
          signature: f.signature.trim(),
        }),
      );
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 404 ? t('copyright.notFound') : errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <section className="perf-req" aria-live="polite">
        <h2>{t('copyright.counterDoneTitle')}</h2>
        <p>{t('copyright.counterDoneBody', {ticket: done.ticket})}</p>
      </section>
    );
  }

  return (
    <section className="perf-req">
      <p className="muted">{t('copyright.counterIntro')}</p>
      <form className="perf-req-form" onSubmit={e => void submit(e)} noValidate>
        <Field id={id('ticket')} label={t('copyright.ticket')} required>
          <input id={id('ticket')} className="input" autoComplete="off" placeholder="TD-XXXX-XXXX-XXXX-XXXX" value={f.ticket} onChange={set('ticket')} />
        </Field>
        <div className="perf-req-row">
          <Field id={id('name')} label={t('copyright.name')} required>
            <input id={id('name')} className="input" autoComplete="name" maxLength={200} value={f.name} onChange={set('name')} />
          </Field>
          <Field id={id('email')} label={t('copyright.email')} required>
            <input id={id('email')} className="input" type="email" autoComplete="email" maxLength={254} value={f.email} onChange={set('email')} />
          </Field>
        </div>
        <Field id={id('address')} label={t('copyright.address')} required>
          <input id={id('address')} className="input" autoComplete="street-address" maxLength={500} value={f.address} onChange={set('address')} />
        </Field>
        <Field id={id('statement')} label={t('copyright.statement')} required hint={t('copyright.statementHint')}>
          <textarea id={id('statement')} className="input" rows={4} maxLength={4000} value={f.statement} onChange={set('statement')} />
        </Field>
        <label className="support-check">
          <input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} />
          <span>{t('copyright.consent')}</span>
        </label>
        <Field id={id('signature')} label={t('copyright.signature')} required hint={t('copyright.signatureHint')}>
          <input id={id('signature')} className="input" maxLength={200} value={f.signature} onChange={set('signature')} />
        </Field>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" className="btn btn-primary perf-req-submit" disabled={busy}>
          {busy ? t('copyright.submitting') : t('copyright.submitCounter')}
        </button>
      </form>
    </section>
  );
}

export default function CopyrightPage() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: Tab = TABS.includes(raw as Tab) ? (raw as Tab) : 'notice';
  const ticket = params.get('ticket') ?? '';

  useEffect(() => {
    document.title = t('title.copyright');
  }, []);

  const go = (next: Tab, tk?: string) => {
    const p = new URLSearchParams();
    if (next !== 'notice') p.set('tab', next);
    if (tk) p.set('ticket', tk);
    setParams(p, {replace: true, preventScrollReset: true});
  };

  return (
    <div className="page support-page">
      <h1 className="page-title">{t('copyright.title')}</h1>
      <p className="muted">
        {t('copyright.intro')}{' '}
        <Link to="/legal/copyright" className="meta-link">
          {t('footer.copyright')}
        </Link>
      </p>
      <div className="support-tabs" role="tablist" aria-label={t('copyright.title')}>
        {TABS.map(x => (
          <button key={x} type="button" role="tab" className="chip" aria-selected={tab === x} onClick={() => go(x, x === 'notice' ? undefined : ticket)}>
            {t(`copyright.tab.${x}`)}
          </button>
        ))}
      </div>
      {tab === 'notice' ? (
        <NoticeForm />
      ) : tab === 'lookup' ? (
        <LookupForm initialTicket={ticket} onCounter={tk => go('counter', tk)} />
      ) : (
        <CounterForm initialTicket={ticket} />
      )}
      <aside className="legal-other">
        <h2>{t('performerReq.otherCases')}</h2>
        <span className="muted">{t('performerReq.copyrightLink')}</span>
        <Link to="/legal/performer-info#request-form">{t('performerReq.cta')}</Link>
      </aside>
    </div>
  );
}

