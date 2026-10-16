import {useEffect, useId, useState, type FormEvent} from 'react';
import {useSearchParams, type MetaFunction} from 'react-router';
import {Link} from '../lib/nav';
import {ApiError, isMissingEndpoint} from '../lib/api';
import {FEEDBACK_CATEGORIES, postFeedback, type FeedbackCategory} from '../lib/compliance';
import {t, useI18n, useT} from '../lib/i18n';
import {seoMeta} from '../lib/seo';
import '../styles/requests.css';
import '../styles/compliance.css';

// 反馈问题 (LegalPage footer; tech plan 3.13 feedback): POST /api/feedback.
// No login. The page the visitor came from (?from=/path, set by the footer
// link) is attached only when they leave the box ticked, and only its path
// is kept (the API drops query strings and fragments). ?category= preselects
// a category (help page links).

export const meta: MetaFunction = ({matches}) => seoMeta({matches, path: '/feedback', title: t('title.feedback'), noindex: true});

const EMAIL = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;

export default function FeedbackPage() {
  const t = useT();
  const {uiLang} = useI18n();
  const uid = useId();
  const [params] = useSearchParams();
  const from = params.get('from') ?? '';
  const fromPath = from.startsWith('/') && !from.startsWith('//') ? from.split(/[?#]/)[0] : '';
  const preset = params.get('category') as FeedbackCategory | null;
  const [category, setCategory] = useState<FeedbackCategory>(preset && FEEDBACK_CATEGORIES.includes(preset) ? preset : 'bug');
  const [message, setMessage] = useState('');
  const [email, setEmail] = useState('');
  const [attach, setAttach] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [receipt, setReceipt] = useState<{id: number} | null>(null);
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    document.title = t('title.feedback');
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    if (!message.trim()) return setError(t('feedback.errMessage'));
    if (email.trim() && !EMAIL.test(email.trim())) return setError(t('feedback.errEmail'));
    setBusy(true);
    try {
      setReceipt(
        await postFeedback({
          category,
          message: message.trim(),
          email: email.trim() || undefined,
          page_url: attach && fromPath ? fromPath : undefined,
          lang: uiLang,
        }),
      );
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 429) setError(t('feedback.rateLimited'));
      else if (isMissingEndpoint(cause)) setUnavailable(true);
      else setError(t('feedback.failed'));
    } finally {
      setBusy(false);
    }
  };

  const id = (s: string) => `${uid}-${s}`;
  return (
    <div className="page support-page">
      <h1 className="page-title">{t('feedback.title')}</h1>
      {unavailable ? (
        <p className="muted" role="status">
          {t('feedback.notDeployed')}
        </p>
      ) : receipt ? (
        <section className="perf-req" aria-live="polite">
          <h2>{t('feedback.doneTitle')}</h2>
          <p>{t('feedback.doneBody', {id: receipt.id})}</p>
          <button
            type="button"
            className="btn btn-secondary perf-req-submit"
            onClick={() => {
              setReceipt(null);
              setMessage('');
            }}
          >
            {t('feedback.again')}
          </button>
        </section>
      ) : (
        <section className="perf-req" aria-labelledby={id('title')}>
          <p className="muted" id={id('title')}>
            {t('feedback.intro')}
          </p>
          <form className="perf-req-form" onSubmit={e => void submit(e)} noValidate>
            <div className="form-field">
              <label htmlFor={id('category')}>{t('feedback.category')}</label>
              <select id={id('category')} className="input" value={category} onChange={e => setCategory(e.target.value as FeedbackCategory)}>
                {FEEDBACK_CATEGORIES.map(c => (
                  <option key={c} value={c}>
                    {t(`feedback.cat.${c}`)}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-field">
              <label htmlFor={id('message')}>
                {t('feedback.message')} <span className="req-mark">*</span>
              </label>
              <textarea id={id('message')} className="input" rows={6} maxLength={4000} value={message} onChange={e => setMessage(e.target.value)} placeholder={t('feedback.messageHint')} />
            </div>
            <div className="form-field">
              <label htmlFor={id('email')}>{t('feedback.email')}</label>
              <input id={id('email')} className="input" type="email" autoComplete="email" maxLength={254} value={email} onChange={e => setEmail(e.target.value)} />
              <span className="muted small">{t('feedback.emailHint')}</span>
            </div>
            {fromPath && (
              <label className="support-check">
                <input type="checkbox" checked={attach} onChange={e => setAttach(e.target.checked)} />
                <span>{t('feedback.page', {path: fromPath})}</span>
              </label>
            )}
            <p className="muted small">
              {t('feedback.privacy')}{' '}
              <Link to="/legal/privacy" className="meta-link">
                {t('footer.privacy')}
              </Link>
            </p>
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <button type="submit" className="btn btn-primary perf-req-submit" disabled={busy}>
              {busy ? t('feedback.submitting') : t('feedback.submit')}
            </button>
          </form>
        </section>
      )}
    </div>
  );
}
