import {useEffect, useState, type FormEvent, type ReactNode} from 'react';
import {useSearchParams} from 'react-router';
import {useNavigate} from '../lib/nav';
import {ApiError, isMissingEndpoint, tokens} from '../lib/api';
import {
  changePassword,
  downloadDataExport,
  getIdentities,
  getSessions,
  requestAccountDeletion,
  revokeOtherSessions,
  revokeSession,
  startOAuth,
  unlinkIdentity,
  updateProfile,
  type Identity,
  type OAuthProvider,
  type SessionInfo,
} from '../lib/account';
import {useAuth} from '../lib/auth';
import {formatDate, timeAgo} from '../lib/format';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import '../styles/account.css';

// Settings › 账号 (PRD 20; tech plan 3.6 / 3.13): nickname, password,
// devices and sessions, Google / Apple linking and account deletion (30-day
// grace, cancelled by signing in again). Every call goes to /api/me/* with
// the user's own token.

const PROVIDER_LABEL: Record<string, string> = {google: 'Google', apple: 'Apple'};
const METHOD_LABEL: Record<string, MessageKey> = {password: 'account.methodPassword', google: 'account.methodGoogle', apple: 'account.methodApple'};
const LINK_ERRORS: Record<string, MessageKey> = {identity_taken: 'account.linkTaken', cancelled: 'login.oauthCancelled', expired: 'login.oauthExpired'};

function errorText(t: ReturnType<typeof useT>, cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 0) return cause.message;
    if (cause.status === 403) return t('account.wrongPassword');
    if (cause.status === 429) return t('login.tooMany');
    if (cause.status === 400 && cause.detail) return cause.detail;
  }
  return t('account.failed');
}

function Group({title, children}: {title: string; children: ReactNode}) {
  return (
    <div className="set-group">
      <h3 className="set-group-title">{title}</h3>
      <div className="set-card">{children}</div>
    </div>
  );
}

function Status({ok, error}: {ok: string; error: string}) {
  return (
    <>
      <span className="sr-status" role="status">
        {ok}
      </span>
      {ok && <span className="set-desc">{ok}</span>}
      {error && (
        <span className="field-error" role="alert">
          {error}
        </span>
      )}
    </>
  );
}

export function AccountSettings() {
  const t = useT();
  const {user, signOut} = useAuth();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const linked = params.get('linked');
  const linkError = params.get('link_error');
  const linkNote = linked ? t('account.linked', {provider: PROVIDER_LABEL[linked] ?? linked}) : '';
  const linkErr = linkError ? t(LINK_ERRORS[linkError] ?? 'login.oauthFailed') : '';

  useEffect(() => {
    if (!linked && !linkError) return;
    const timer = setTimeout(() => {
      const next = new URLSearchParams(params);
      next.delete('linked');
      next.delete('link_error');
      setParams(next, {replace: true});
    }, 6000);
    return () => clearTimeout(timer);
  }, [linked, linkError, params, setParams]);

  return (
    <>
      <Group title={t('settings.basicInfo')}>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.email')}</span>
            <span className="set-desc">{t('settings.emailDesc')}</span>
          </div>
          <span className="set-value">{user?.email ?? ''}</span>
        </div>
        <NicknameRow />
      </Group>
      <PasswordGroup />
      <SessionsGroup />
      <IdentitiesGroup note={linkNote} error={linkErr} />
      <Group title={t('account.dangerGroup')}>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.signOut')}</span>
            <span className="set-desc">{t('account.signOutDesc')}</span>
          </div>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => {
              signOut();
              navigate('/');
            }}
          >
            {t('settings.signOut')}
          </button>
        </div>
        <DeleteAccountRow />
      </Group>
    </>
  );
}

function NicknameRow() {
  const t = useT();
  const {user, refresh} = useAuth();
  const [value, setValue] = useState(user?.display_name ?? '');
  const [busy, setBusy] = useState(false);
  const [ok, setOk] = useState('');
  const [error, setError] = useState('');

  useEffect(() => setValue(user?.display_name ?? ''), [user?.display_name]);
  if (user?.display_name === undefined) return null; // older backend

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setOk('');
    setError('');
    try {
      await updateProfile(value);
      setOk(t('account.saved'));
      refresh();
    } catch (cause) {
      setError(errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="set-row set-row-stack" onSubmit={save}>
      <div className="set-text">
        <label className="set-label" htmlFor="set-nickname">
          {t('login.nickname')}
        </label>
        <span className="set-desc">{t('account.nicknameDesc')}</span>
        <Status ok={ok} error={error} />
      </div>
      <div className="set-form-actions">
        <input id="set-nickname" className="set-input" value={value} maxLength={40} onChange={e => setValue(e.target.value)} autoComplete="nickname" />
        <button type="submit" className="btn btn-secondary" disabled={busy || value === (user?.display_name ?? '')}>
          {t('account.save')}
        </button>
      </div>
    </form>
  );
}

function PasswordGroup() {
  const t = useT();
  const {user} = useAuth();
  const [open, setOpen] = useState(false);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [ok, setOk] = useState('');
  const [error, setError] = useState('');
  if (user?.has_password === undefined) return null;
  const hasPassword = user.has_password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setOk('');
    setError('');
    if (next.length < 8) return setError(t('login.passwordShort'));
    if (next !== confirm) return setError(t('reset.mismatch'));
    setBusy(true);
    try {
      await changePassword(current, next);
      setOk(t('account.passwordChanged'));
      setOpen(false);
      setCurrent('');
      setNext('');
      setConfirm('');
    } catch (cause) {
      setError(errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Group title={t('account.passwordGroup')}>
      <div className="set-row set-row-stack">
        <div className="set-row-inline">
          <div className="set-text">
            <span className="set-label">{hasPassword ? t('account.changePassword') : t('account.setPassword')}</span>
            <span className="set-desc">{t('account.passwordDesc')}</span>
            {!open && <Status ok={ok} error={error} />}
          </div>
          {!open && (
            <button type="button" className="btn btn-secondary" onClick={() => setOpen(true)}>
              {hasPassword ? t('account.change') : t('account.set')}
            </button>
          )}
        </div>
        {open && (
          <form className="set-form" onSubmit={submit}>
            {hasPassword && (
              <label className="field">
                <span>{t('account.currentPassword')}</span>
                <input className="set-input" type="password" value={current} onChange={e => setCurrent(e.target.value)} autoComplete="current-password" required />
              </label>
            )}
            <label className="field">
              <span>{t('reset.newPassword')}</span>
              <input className="set-input" type="password" value={next} onChange={e => setNext(e.target.value)} autoComplete="new-password" minLength={8} required />
            </label>
            <label className="field">
              <span>{t('reset.confirm')}</span>
              <input className="set-input" type="password" value={confirm} onChange={e => setConfirm(e.target.value)} autoComplete="new-password" minLength={8} required />
            </label>
            <Status ok="" error={error} />
            <div className="set-form-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setOpen(false)} disabled={busy}>
                {t('settings.cancel')}
              </button>
              <button type="submit" className="btn btn-primary" disabled={busy}>
                {busy ? t('login.wait') : t('account.save')}
              </button>
            </div>
          </form>
        )}
      </div>
    </Group>
  );
}

function SessionsGroup() {
  const t = useT();
  const [items, setItems] = useState<SessionInfo[] | null>(null);
  const [missing, setMissing] = useState(false);
  const [busy, setBusy] = useState<number | 'all' | null>(null);
  const [ok, setOk] = useState('');
  const [error, setError] = useState('');
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    getSessions(controller.signal).then(setItems, cause => {
      if (controller.signal.aborted) return;
      if (isMissingEndpoint(cause)) setMissing(true);
      else setError(errorText(t, cause));
    });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nonce]);
  if (missing) return null;

  const revoke = async (id: number | 'all') => {
    setBusy(id);
    setOk('');
    setError('');
    try {
      if (id === 'all') {
        const r = await revokeOtherSessions();
        setOk(t('account.revokedOthers', {n: r?.revoked ?? 0}));
      } else {
        await revokeSession(id);
        setOk(t('account.revoked'));
      }
      setNonce(n => n + 1);
    } catch (cause) {
      setError(errorText(t, cause));
    } finally {
      setBusy(null);
    }
  };
  const others = (items ?? []).filter(s => !s.current).length;
  return (
    <Group title={t('account.devicesGroup')}>
      <ul className="session-list" aria-busy={items === null || undefined}>
        {(items ?? []).map(s => (
          <li key={s.id} className="session-item">
            <div className="set-text">
              <span className="set-label">
                {s.device || t('account.unknownDevice')}
                {s.current && <span className="session-badge">{t('account.thisDevice')}</span>}
              </span>
              <span className="set-desc">
                {t('account.sessionMeta', {
                  method: t(METHOD_LABEL[s.method] ?? 'account.methodPassword'),
                  seen: timeAgo(s.last_seen_at),
                  since: formatDate(s.created_at),
                })}
                {!s.remember && ` · ${t('account.browserSession')}`}
              </span>
            </div>
            {!s.current && (
              <button type="button" className="btn btn-secondary" disabled={busy !== null} onClick={() => void revoke(s.id)}>
                {t('account.signOutDevice')}
              </button>
            )}
          </li>
        ))}
      </ul>
      <div className="set-row">
        <div className="set-text">
          <span className="set-label">{t('account.signOutOthers')}</span>
          <span className="set-desc">{t('account.signOutOthersDesc')}</span>
          <Status ok={ok} error={error} />
        </div>
        <button type="button" className="btn btn-secondary" disabled={busy !== null || others === 0} onClick={() => void revoke('all')}>
          {t('account.signOutOthersButton')}
        </button>
      </div>
    </Group>
  );
}

function IdentitiesGroup({note, error: linkError}: {note: string; error: string}) {
  const t = useT();
  const {user, refresh} = useAuth();
  const [data, setData] = useState<{items: Identity[]; available: string[]} | null>(null);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    getIdentities(controller.signal).then(setData, () => {});
    return () => controller.abort();
  }, [nonce]);
  if (!data) return null;
  const providers = Array.from(new Set([...data.available, ...data.items.map(i => i.provider)])).filter(p => p === 'google' || p === 'apple');
  if (providers.length === 0) return null;

  const link = async (p: OAuthProvider) => {
    setBusy(p);
    setError('');
    try {
      await startOAuth(p, {link: true, next: '/settings?s=account'});
    } catch (cause) {
      setError(errorText(t, cause));
      setBusy('');
    }
  };
  const unlink = async (p: string) => {
    setBusy(p);
    setError('');
    try {
      await unlinkIdentity(p);
      setNonce(n => n + 1);
      refresh();
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 409 ? t('account.lastMethod') : errorText(t, cause));
    } finally {
      setBusy('');
    }
  };
  return (
    <Group title={t('account.providersGroup')}>
      {providers.map(p => {
        const linked = data.items.find(i => i.provider === p);
        return (
          <div className="set-row" key={p}>
            <div className="set-text">
              <span className="set-label">{PROVIDER_LABEL[p]}</span>
              <span className="set-desc">{linked ? t('account.linkedAs', {email: linked.email || PROVIDER_LABEL[p]}) : t('account.notLinked')}</span>
            </div>
            {linked ? (
              <button type="button" className="btn btn-secondary" disabled={!!busy || (!user?.has_password && data.items.length <= 1)} onClick={() => void unlink(p)}>
                {t('account.unlink')}
              </button>
            ) : (
              data.available.includes(p) && (
                <button type="button" className="btn btn-secondary" disabled={!!busy} onClick={() => void link(p as OAuthProvider)}>
                  {t('account.link')}
                </button>
              )
            )}
          </div>
        );
      })}
      {(note || error || linkError) && (
        <p className="set-note" role="status">
          {note}
          {(error || linkError) && <span className="field-error">{error || linkError}</span>}
        </p>
      )}
    </Group>
  );
}

function DeleteAccountRow() {
  const t = useT();
  const {user, signOut} = useAuth();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState('');
  if (user?.has_password === undefined) return null;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const r = await requestAccountDeletion(password);
      setDone(t('account.deletionScheduled', {date: formatDate(r?.purge_after)}));
      // Every session is gone on the server; drop this one locally.
      tokens.setUser(null);
      setTimeout(() => {
        signOut();
        navigate('/');
      }, 4000);
    } catch (cause) {
      setError(errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="set-row set-row-stack">
      <div className="set-row-inline">
        <div className="set-text">
          <span className="set-label">{t('account.delete')}</span>
          <span className="set-desc">{t('account.deleteDesc')}</span>
        </div>
        {!confirming && !done && (
          <button type="button" className="btn btn-secondary btn-danger" onClick={() => setConfirming(true)}>
            {t('account.delete')}
          </button>
        )}
      </div>
      {done && (
        <p className="form-ok" role="status">
          {done}
        </p>
      )}
      {confirming && !done && (
        <form className="set-form" onSubmit={submit} aria-label={t('account.delete')}>
          <span className="set-desc">{t('account.deleteConfirm')}</span>
          {user.has_password && (
            <label className="field">
              <span>{t('account.currentPassword')}</span>
              <input className="set-input" type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="current-password" required />
            </label>
          )}
          {error && (
            <span className="field-error" role="alert">
              {error}
            </span>
          )}
          <div className="set-form-actions">
            <button type="button" className="btn btn-secondary" onClick={() => setConfirming(false)} disabled={busy}>
              {t('settings.cancel')}
            </button>
            <button type="submit" className="btn btn-primary btn-danger-solid" disabled={busy}>
              {busy ? t('login.wait') : t('account.deleteButton')}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

/** Settings › 隐私与数据 › 下载我的数据 (GET /api/me/export, 3 per day). */
export function DataExportRow() {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const [ok, setOk] = useState('');
  const [error, setError] = useState('');
  const run = async () => {
    setBusy(true);
    setOk('');
    setError('');
    try {
      await downloadDataExport();
      setOk(t('account.exportDone'));
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 429 ? t('account.exportLimit') : isMissingEndpoint(cause) ? t('account.exportMissing') : errorText(t, cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="set-row">
      <div className="set-text">
        <span className="set-label">{t('account.export')}</span>
        <span className="set-desc">{t('account.exportDesc')}</span>
        <Status ok={ok} error={error} />
      </div>
      <button type="button" className="btn btn-secondary" onClick={run} disabled={busy}>
        {busy ? t('account.exporting') : t('account.exportButton')}
      </button>
    </div>
  );
}
