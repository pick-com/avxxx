import {useEffect, useId, useState, type FormEvent} from 'react';
import {ApiError, api, isAbort, isMissingEndpoint, listPerformerPage} from '../lib/api';
import {formatDate} from '../lib/format';
import {useT} from '../lib/i18n';
import '../styles/requests.css';

// 演员信息更正与隐藏 (CopyrightForm 其他情况; PRD 6, 15): the public intake
// POST /api/performer-requests (admin-performers backend). No login; the
// performer is picked by name (slug sent), kind decides the SLA (3 days for
// privacy requests, 7 for corrections), evidence (role, message, ≤ 5 links)
// is only visible to admins. 429 = rate limited; 404 = not deployed.

const KINDS = ['correct_field', 'hide_birth_date', 'remove_photo', 'other'] as const;
type Kind = (typeof KINDS)[number];
const FIELDS = ['canonical_name', 'canonical_name_ja', 'birth_date', 'birth_place', 'debut_date', 'height_cm', 'cup_size', 'biography'] as const;
type Field = (typeof FIELDS)[number];
const ROLES = ['self', 'agency', 'other'] as const;
const MAX_LINKS = 5;

type Receipt = {id: number; kind: string; status: string; due_at: string};
type PickedPerformer = {slug: string; name: string};

export function PerformerRequestForm() {
  const t = useT();
  const uid = useId();
  const [performer, setPerformer] = useState<PickedPerformer | null>(null);
  const [lookup, setLookup] = useState('');
  const [matches, setMatches] = useState<PickedPerformer[] | null>(null);
  const [kind, setKind] = useState<Kind>('correct_field');
  const [field, setField] = useState<Field | ''>('');
  const [value, setValue] = useState('');
  const [role, setRole] = useState<(typeof ROLES)[number]>('self');
  const [contact, setContact] = useState('');
  const [message, setMessage] = useState('');
  const [links, setLinks] = useState<string[]>(['']);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    const q = lookup.trim();
    if (performer || !q) {
      setMatches(null);
      return;
    }
    const ctrl = new AbortController();
    const timer = window.setTimeout(() => {
      listPerformerPage('name', 6, null, q, ctrl.signal).then(
        page => !ctrl.signal.aborted && setMatches(page.items.map(p => ({slug: p.slug, name: p.canonical_name_ja ? `${p.canonical_name}（${p.canonical_name_ja}）` : p.canonical_name}))),
        e => !isAbort(e) && !ctrl.signal.aborted && setMatches([]),
      );
    }, 250);
    return () => {
      ctrl.abort();
      window.clearTimeout(timer);
    };
  }, [lookup, performer]);

  const reset = () => {
    setReceipt(null);
    setPerformer(null);
    setLookup('');
    setField('');
    setValue('');
    setMessage('');
    setLinks(['']);
    setError('');
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    if (!performer) return setError(t('performerReq.errPerformer'));
    if (!contact.trim()) return setError(t('performerReq.errContact'));
    if (kind === 'correct_field' && (!field || !value.trim())) return setError(t('performerReq.errField'));
    const cleanLinks = links.map(l => l.trim()).filter(Boolean);
    if (cleanLinks.some(l => !/^https?:\/\/\S+$/i.test(l))) return setError(t('performerReq.errLinks'));
    const body: Record<string, unknown> = {performer: performer.slug, kind, contact: contact.trim(), role, message: message.trim(), links: cleanLinks};
    if (kind === 'correct_field') {
      body.field = field;
      body.value = field === 'height_cm' ? Number(value.trim()) : value.trim();
    }
    setBusy(true);
    try {
      setReceipt(await api<Receipt>('performer-requests', {method: 'POST', body}));
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 429) setError(t('performerReq.rateLimited'));
      else if (isMissingEndpoint(cause)) setUnavailable(true);
      else if (cause instanceof ApiError && cause.status === 400 && cause.detail) setError(t('performerReq.invalid', {detail: cause.detail}));
      else setError(t('performerReq.failed'));
    } finally {
      setBusy(false);
    }
  };

  if (unavailable) {
    return (
      <section id="request-form" className="legal-section perf-req">
        <h2>{t('performerReq.title')}</h2>
        <p className="muted" role="status">
          {t('performerReq.notDeployed')}
        </p>
      </section>
    );
  }

  if (receipt) {
    return (
      <section id="request-form" className="legal-section perf-req" aria-live="polite">
        <h2>{t('performerReq.doneTitle')}</h2>
        <p>{t('performerReq.doneBody', {id: receipt.id, due: formatDate(receipt.due_at)})}</p>
        <button type="button" className="btn btn-secondary" onClick={reset}>
          {t('performerReq.again')}
        </button>
      </section>
    );
  }

  const id = (s: string) => `${uid}-${s}`;
  return (
    <section id="request-form" className="legal-section perf-req" aria-labelledby={id('title')}>
      <h2 id={id('title')}>{t('performerReq.title')}</h2>
      <p className="muted">{t('performerReq.intro')}</p>
      <form className="perf-req-form" onSubmit={e => void submit(e)} noValidate>
        <div className="form-field">
          <label htmlFor={id('performer')}>
            {t('performerReq.performer')} <span className="req-mark">*</span>
          </label>
          {performer ? (
            <div className="perf-req-picked">
              <span>{t('performerReq.performerPicked', {name: performer.name})}</span>
              <button type="button" className="text-btn" onClick={() => (setPerformer(null), setLookup(''))}>
                {t('performerReq.performerChange')}
              </button>
            </div>
          ) : (
            <>
              <input id={id('performer')} className="input" value={lookup} onChange={e => setLookup(e.target.value)} placeholder={t('performerReq.performerHint')} autoComplete="off" />
              {matches && (
                <div className="chip-row chip-row-wrap perf-req-matches">
                  {matches.length === 0 && <span className="muted">{t('performerReq.performerNone')}</span>}
                  {matches.map(m => (
                    <button key={m.slug} type="button" className="chip" onClick={() => setPerformer(m)}>
                      {m.name}
                    </button>
                  ))}
                </div>
              )}
            </>
          )}
        </div>

        <fieldset className="form-field">
          <legend>
            {t('performerReq.kind')} <span className="req-mark">*</span>
          </legend>
          <div className="perf-req-kinds">
            {KINDS.map(k => (
              <label key={k} className={`perf-req-kind ${kind === k ? 'is-on' : ''}`}>
                <input type="radio" name="kind" value={k} checked={kind === k} onChange={() => setKind(k)} />
                <span>
                  <strong>{t(`performerReq.kind.${k}`)}</strong>
                  <span className="muted">{t(`performerReq.kindHint.${k}`)}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>

        {kind === 'correct_field' && (
          <div className="perf-req-row">
            <div className="form-field">
              <label htmlFor={id('field')}>{t('performerReq.field')}</label>
              <select id={id('field')} className="input" value={field} onChange={e => setField(e.target.value as Field)}>
                <option value="">—</option>
                {FIELDS.map(f => (
                  <option key={f} value={f}>
                    {t(`performerReq.field.${f}`)}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-field">
              <label htmlFor={id('value')}>{t('performerReq.value')}</label>
              <input
                id={id('value')}
                className="input"
                type={field === 'birth_date' || field === 'debut_date' ? 'date' : field === 'height_cm' ? 'number' : 'text'}
                value={value}
                onChange={e => setValue(e.target.value)}
                maxLength={field === 'biography' ? 2000 : 200}
              />
              {(field === 'birth_date' || field === 'debut_date') && <span className="muted small">{t('performerReq.valueDateHint')}</span>}
            </div>
          </div>
        )}

        <div className="perf-req-row">
          <div className="form-field">
            <label htmlFor={id('role')}>{t('performerReq.role')}</label>
            <select id={id('role')} className="input" value={role} onChange={e => setRole(e.target.value as (typeof ROLES)[number])}>
              {ROLES.map(r => (
                <option key={r} value={r}>
                  {t(`performerReq.role.${r}`)}
                </option>
              ))}
            </select>
          </div>
          <div className="form-field">
            <label htmlFor={id('contact')}>
              {t('performerReq.contact')} <span className="req-mark">*</span>
            </label>
            <input id={id('contact')} className="input" value={contact} onChange={e => setContact(e.target.value)} placeholder={t('performerReq.contactHint')} maxLength={200} autoComplete="email" />
          </div>
        </div>

        <div className="form-field">
          <label htmlFor={id('message')}>{t('performerReq.message')}</label>
          <textarea id={id('message')} className="input" rows={4} value={message} onChange={e => setMessage(e.target.value)} placeholder={t('performerReq.messageHint')} maxLength={2000} />
        </div>

        <div className="form-field">
          <span className="form-label">{t('performerReq.links')}</span>
          {links.map((l, i) => (
            <div key={i} className="perf-req-link">
              <input
                className="input"
                type="url"
                inputMode="url"
                value={l}
                aria-label={`${t('performerReq.links')} ${i + 1}`}
                placeholder={t('performerReq.linkPlaceholder')}
                onChange={e => setLinks(prev => prev.map((x, j) => (j === i ? e.target.value : x)))}
                maxLength={500}
              />
              {links.length > 1 && (
                <button type="button" className="icon-text-btn" aria-label={t('performerReq.removeLink')} onClick={() => setLinks(prev => prev.filter((_, j) => j !== i))}>
                  ×
                </button>
              )}
            </div>
          ))}
          {links.length < MAX_LINKS && (
            <button type="button" className="text-btn" onClick={() => setLinks(prev => [...prev, ''])}>
              + {t('performerReq.addLink')}
            </button>
          )}
        </div>

        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" className="btn btn-primary perf-req-submit" disabled={busy}>
          {busy ? t('performerReq.submitting') : t('performerReq.submit')}
        </button>
      </form>
    </section>
  );
}
