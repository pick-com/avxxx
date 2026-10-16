import {useEffect, useState, type ReactNode} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {Icon, type IconName} from '../components/Icon';
import {EmptyState, ErrorBanner, BlockSkeleton} from '../components/States';
import {Switch, SwitchRow} from '../components/Switch';
import {clearWatchHistory, type LangCode, type NotifyChannel, type NotifyKind, type QualityPref, type UserSettings} from '../lib/api';
import {useAuth} from '../lib/auth';
import {useConsent} from '../lib/consent';
import {browserTimezone, useSettings} from '../lib/settings';
import {themeLabels, useTheme, type ThemePref} from '../lib/theme';
import {useMediaQuery} from '../lib/useMediaQuery';
import {LANGS, LANG_LABELS} from '../lib/langs';
import {t as translate, useI18n, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';

export const meta = () => [{title: 'REEL · 设置'}];

// Settings / MobileSettings on GET / PATCH /api/me/settings. Every control
// saves on change (optimistic, per-field error under the control).
//
// Not built (no backend or decision yet):
// TODO(tech plan 3.6 / 3.13): 昵称、修改密码、已登录设备、第三方登录、注销账号.
// TODO(tech plan 3.13): 跳过片头、默认片源、字幕大小、配音、清除搜索记录、下载我的数据、用观看记录改进推荐.
// TODO(PRD 17): 「上线前一天」提醒 and a configurable daily cap — the fan-out
// sends at go-live and merges to one notice per channel per day.
// TODO(PRD 16 / 20): 广告偏好 and 青少年模式 sections (青少年模式 positioning is undecided).

type SectionKey = 'account' | 'playback' | 'language' | 'notify' | 'privacy';
const SECTION_DEFS: {key: SectionKey; title: MessageKey; desc: MessageKey; icon: IconName}[] = [
  {key: 'account', title: 'settings.account', desc: 'settings.accountDesc', icon: 'me'},
  {key: 'playback', title: 'settings.playback', desc: 'settings.playbackDesc', icon: 'play'},
  {key: 'language', title: 'settings.language', desc: 'settings.languageDesc', icon: 'globe'},
  {key: 'notify', title: 'settings.notify', desc: 'settings.notifyDesc', icon: 'bell'},
  {key: 'privacy', title: 'settings.privacy', desc: 'settings.privacyDesc', icon: 'lock'},
];

const QUALITY_PREFS: QualityPref[] = ['auto', '2160p', '1080p', '720p', '480p', '360p'];
const qualityLabel = (q: QualityPref) => (q === 'auto' ? translate('settings.qualityAuto') : q === '2160p' ? translate('settings.quality4k') : q);
const KIND_LABELS: [NotifyKind, MessageKey, MessageKey | null][] = [
  ['new_work', 'settings.kindNewWork', 'settings.kindNewWorkDesc'],
  ['reservation', 'settings.kindReservation', 'settings.kindReservationDesc'],
  ['comment_reply', 'settings.kindCommentReply', null],
  ['favorite_update', 'settings.kindFavoriteUpdate', 'settings.kindFavoriteUpdateDesc'],
];
const CHANNEL_LABELS: [NotifyChannel, MessageKey][] = [
  ['in_app', 'settings.channelInApp'],
  ['push', 'settings.channelPush'],
  ['email', 'settings.channelEmail'],
];
const DEFAULT_QUIET = {start: '22:00', end: '08:00'};

export default function SettingsPage() {
  const t = useT();
  const SECTIONS = SECTION_DEFS.map(s => ({...s, title: t(s.title), desc: t(s.desc)}));
  const {status: auth} = useAuth();
  const {status, error, reload} = useSettings();
  const [params, setParams] = useSearchParams();
  const mobile = useMediaQuery('(max-width: 767px)');
  const raw = params.get('s') as SectionKey | null;
  const section = SECTIONS.find(s => s.key === raw) ?? null;

  if (auth === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="settings" title={t('settings.signInTitle')} body={t('settings.signInBody')} actions={[{to: '/login?next=%2Fsettings', label: t('auth.login')}]} />
      </div>
    );
  }
  if (status === 'missing') {
    return (
      <div className="page">
        <EmptyState icon="settings" title={t('settings.missingTitle')} body={t('settings.missingBody')} actions={[{to: '/me', label: t('settings.backToMe')}]} />
      </div>
    );
  }

  const current = section ?? (mobile ? null : SECTIONS[0]);
  const pick = (key: SectionKey) => setParams({s: key}, {replace: !mobile});

  return (
    <div className="page settings-page">
      <h1 className="page-title">{mobile && current ? current.title : t('settings.title')}</h1>
      {status === 'error' && error && <ErrorBanner error={error} onRetry={reload} />}
      <div className="settings-layout">
        {(!mobile || !current) && (
          <nav className="settings-nav" aria-label={t('settings.nav')}>
            {SECTIONS.map(s => (
              <button
                key={s.key}
                type="button"
                className="settings-nav-item"
                aria-current={current?.key === s.key ? 'page' : undefined}
                onClick={() => pick(s.key)}
              >
                <Icon name={s.icon} />
                <span className="settings-nav-text">
                  <span>{s.title}</span>
                  {mobile && <span className="set-desc">{s.desc}</span>}
                </span>
                {mobile && <Icon name="chevronRight" size={18} />}
              </button>
            ))}
          </nav>
        )}
        {current && (
          <section className="settings-panel" aria-labelledby="settings-head">
            {mobile && (
              <Link to="/settings" className="text-btn settings-back">
                <Icon name="back" size={16} /> {t('settings.all')}
              </Link>
            )}
            <h2 id="settings-head" className={mobile ? 'sr-only' : 'settings-head'}>
              {current.title}
            </h2>
            <p className="muted">{current.desc}</p>
            <SectionBody section={current.key} />
          </section>
        )}
      </div>
    </div>
  );
}

function SectionBody({section}: {section: SectionKey}) {
  const {status, settings} = useSettings();
  if (section === 'account') return <AccountSection />;
  if (status === 'loading' || !settings) return status === 'error' ? null : <BlockSkeleton lines={5} />;
  switch (section) {
    case 'playback':
      return <PlaybackSection s={settings} />;
    case 'language':
      return <LanguageSection s={settings} />;
    case 'notify':
      return <NotifySection s={settings} />;
    case 'privacy':
      return <PrivacySection s={settings} />;
  }
  return null;
}

function Group({title, children}: {title: string; children: ReactNode}) {
  return (
    <div className="set-group">
      <h3 className="set-group-title">{title}</h3>
      <div className="set-card">{children}</div>
    </div>
  );
}

function FieldError({k}: {k: string}) {
  const {fieldErrors} = useSettings();
  return fieldErrors[k] ? (
    <span className="field-error" role="alert">
      {fieldErrors[k]}
    </span>
  ) : null;
}

/** Boolean setting bound to a top-level key. */
function BoolRow({k, label, description, s, invert = false}: {
  k: 'data_saver' | 'autoplay' | 'remember_rate' | 'history_paused';
  label: string;
  description?: string;
  s: UserSettings;
  invert?: boolean;
}) {
  const {save, fieldErrors, pending} = useSettings();
  const value = invert ? !s[k] : s[k];
  return (
    <SwitchRow
      label={label}
      description={description}
      checked={value}
      busy={pending.has(k)}
      error={fieldErrors[k]}
      onChange={next => {
        const stored = invert ? !next : next;
        save(k, {[k]: stored}, cur => ({...cur, [k]: stored}));
      }}
    />
  );
}

function SelectRow<T extends string>({id, label, description, value, options, onChange, errorKey}: {
  id: string;
  label: string;
  description?: string;
  value: T;
  options: [T, string][];
  onChange: (value: T) => void;
  errorKey: string;
}) {
  return (
    <div className="set-row">
      <div className="set-text">
        <label className="set-label" htmlFor={id}>
          {label}
        </label>
        {description && <span className="set-desc">{description}</span>}
        <FieldError k={errorKey} />
      </div>
      <select id={id} className="set-select" value={value} onChange={e => onChange(e.target.value as T)}>
        {options.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </select>
    </div>
  );
}

function AccountSection() {
  const t = useT();
  const {user, signOut} = useAuth();
  const navigate = useNavigate();
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
      </Group>
      <p className="muted">{t('settings.accountSoon')}</p>
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
    </>
  );
}

function PlaybackSection({s}: {s: UserSettings}) {
  const t = useT();
  const {save} = useSettings();
  return (
    <>
      <Group title={t('settings.qualityGroup')}>
        <SelectRow
          id="set-quality"
          label={t('settings.quality')}
          description={t('settings.qualityDesc')}
          value={s.quality_pref}
          options={QUALITY_PREFS.map(q => [q, qualityLabel(q)])}
          errorKey="quality_pref"
          onChange={v => save('quality_pref', {quality_pref: v}, cur => ({...cur, quality_pref: v}))}
        />
        <BoolRow k="data_saver" label={t('settings.dataSaver')} description={t('settings.dataSaverDesc')} s={s} />
      </Group>
      <Group title={t('settings.habits')}>
        <BoolRow k="autoplay" label={t('settings.autoplay')} description={t('settings.autoplayDesc')} s={s} />
        <BoolRow k="remember_rate" label={t('settings.rememberRate')} description={t('settings.rememberRateDesc')} s={s} />
      </Group>
    </>
  );
}

function LanguageSection({s}: {s: UserSettings}) {
  const t = useT();
  const {setUiLang} = useI18n();
  const {save, fieldErrors} = useSettings();
  const {setPref} = useTheme();
  const langOptions = LANGS.map(l => [l, LANG_LABELS[l]] as [LangCode, string]);
  const toggleSub = (lang: LangCode) => {
    const next = s.subtitle_langs.includes(lang) ? s.subtitle_langs.filter(l => l !== lang) : [...s.subtitle_langs, lang];
    save('subtitle_langs', {subtitle_langs: next}, cur => ({...cur, subtitle_langs: next}));
  };
  return (
    <>
      <Group title={t('settings.languageGroup')}>
        <SelectRow
          id="set-ui-lang"
          label={t('settings.uiLang')}
          description={t('settings.uiLangDesc')}
          value={s.ui_lang}
          options={langOptions}
          errorKey="ui_lang"
          onChange={v => setUiLang(v)}
        />
        <SelectRow
          id="set-content-lang"
          label={t('settings.contentLang')}
          description={t('settings.contentLangDesc')}
          value={s.content_lang}
          options={langOptions}
          errorKey="content_lang"
          onChange={v => save('content_lang', {content_lang: v}, cur => ({...cur, content_lang: v}))}
        />
        <div className="set-row set-row-stack">
          <div className="set-text">
            <span className="set-label" id="sub-langs-label">
              {t('settings.subtitleOrder')}
            </span>
            <span className="set-desc">
              {t('settings.subtitleHint', {
                order: s.subtitle_langs.length > 0 ? s.subtitle_langs.map(l => LANG_LABELS[l] ?? l).join(' → ') : t('settings.subtitleUnset'),
              })}
            </span>
            <FieldError k="subtitle_langs" />
          </div>
          <div className="chip-row chip-row-wrap" role="group" aria-labelledby="sub-langs-label">
            {LANGS.map(l => {
              const index = s.subtitle_langs.indexOf(l);
              return (
                <button key={l} type="button" className={index >= 0 ? 'chip chip-lg chip-on' : 'chip chip-lg'} aria-pressed={index >= 0} onClick={() => toggleSub(l)}>
                  {index >= 0 && <span className="chip-count">{index + 1}</span>}
                  {LANG_LABELS[l]}
                </button>
              );
            })}
          </div>
        </div>
      </Group>
      <Group title={t('settings.appearance')}>
        <div className="set-row set-row-stack">
          <div className="set-text">
            <span className="set-label" id="theme-label">
              {t('settings.theme')}
            </span>
            <span className="set-desc">{t('settings.themeDesc')}</span>
            {fieldErrors.theme && (
              <span className="field-error" role="alert">
                {fieldErrors.theme}
              </span>
            )}
          </div>
          <div className="segmented" role="radiogroup" aria-labelledby="theme-label">
            {(['system', 'dark', 'light'] as ThemePref[]).map(p => (
              <button
                key={p}
                type="button"
                role="radio"
                aria-checked={s.theme === p}
                className="segmented-item"
                onClick={() => {
                  // The settings sync PATCHes theme for user-originated changes.
                  setPref(p, 'user');
                }}
              >
                {themeLabels[p]}
              </button>
            ))}
          </div>
        </div>
      </Group>
    </>
  );
}

function NotifySection({s}: {s: UserSettings}) {
  const t = useT();
  const {save, pending} = useSettings();
  const tz = browserTimezone();
  const setChannel = (kind: NotifyKind, ch: NotifyChannel, on: boolean) =>
    save(`notify_prefs.${kind}`, {notify_prefs: {[kind]: {[ch]: on}}}, cur => ({
      ...cur,
      notify_prefs: {...cur.notify_prefs, [kind]: {...cur.notify_prefs[kind], [ch]: on}},
    }));

  return (
    <>
      <Group title={t('settings.notifyGroup')}>
        {KIND_LABELS.map(([kind, labelKey, descKey]) => {
          const label = t(labelKey);
          const desc = descKey ? t(descKey) : '';
          return (
          <div className="set-row set-row-stack" key={kind}>
            <div className="set-text">
              <span className="set-label" id={`kind-${kind}`}>
                {label}
              </span>
              {desc && <span className="set-desc">{desc}</span>}
              <FieldError k={`notify_prefs.${kind}`} />
            </div>
            <div className="chip-row chip-row-wrap" role="group" aria-labelledby={`kind-${kind}`}>
              {CHANNEL_LABELS.map(([ch, chKey]) => {
                const chLabel = t(chKey);
                const on = s.notify_prefs?.[kind]?.[ch] ?? false;
                return (
                  <button
                    key={ch}
                    type="button"
                    className={on ? 'chip chip-lg chip-on' : 'chip chip-lg'}
                    aria-pressed={on}
                    aria-label={t('settings.channelLabel', {kind: label, channel: chLabel})}
                    aria-busy={pending.has(`notify_prefs.${kind}`) || undefined}
                    onClick={() => setChannel(kind, ch, !on)}
                  >
                    {on && <Icon name="check" size={14} />}
                    {chLabel}
                  </button>
                );
              })}
            </div>
          </div>
          );
        })}
        <p className="set-note">{t('settings.pushNote')}</p>
      </Group>
      <Group title={t('settings.timing')}>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.when')}</span>
            <span className="set-desc">{t('settings.whenDesc')}</span>
          </div>
          <span className="set-value">{t('settings.whenValue')}</span>
        </div>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.dailyCap')}</span>
            <span className="set-desc">{t('settings.dailyCapDesc')}</span>
          </div>
          <span className="set-value">{t('settings.dailyCapValue')}</span>
        </div>
        <QuietHours s={s} />
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.timezone')}</span>
            <span className="set-desc">{t('settings.timezoneDesc')}</span>
            <FieldError k="timezone" />
          </div>
          <span className="set-value">
            {s.timezone}
            {tz !== s.timezone && (
              <button type="button" className="text-btn" onClick={() => save('timezone', {timezone: tz}, cur => ({...cur, timezone: tz}))}>
                {t('settings.useLocalTz', {tz})}
              </button>
            )}
          </span>
        </div>
      </Group>
    </>
  );
}

const CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/;

function QuietHours({s}: {s: UserSettings}) {
  const t = useT();
  const {save, fieldErrors, pending} = useSettings();
  const on = s.quiet_hours !== null;
  const [draft, setDraft] = useState(s.quiet_hours ?? DEFAULT_QUIET);
  const [local, setLocal] = useState('');

  useEffect(() => {
    if (s.quiet_hours) setDraft(s.quiet_hours);
  }, [s.quiet_hours]);

  const commit = (next: {start: string; end: string}) => {
    setLocal('');
    if (!CLOCK.test(next.start) || !CLOCK.test(next.end)) {
      setLocal(t('settings.quietFormat'));
      return;
    }
    if (next.start === next.end) {
      setLocal(t('settings.quietSame'));
      return;
    }
    if (s.quiet_hours && next.start === s.quiet_hours.start && next.end === s.quiet_hours.end) return;
    save('quiet_hours', {quiet_hours: next}, cur => ({...cur, quiet_hours: next}));
  };

  const error = local || fieldErrors.quiet_hours;
  return (
    <div className="set-row set-row-stack">
      <div className="set-row-inline">
        <div className="set-text">
          <span className="set-label" id="quiet-label">
            {t('settings.quiet')}
          </span>
          <span className="set-desc">{t('settings.quietDesc')}</span>
        </div>
        <Switch
          checked={on}
          label={t('settings.quiet')}
          busy={pending.has('quiet_hours')}
          onChange={next => {
            setLocal('');
            const value = next ? draft : null;
            save('quiet_hours', {quiet_hours: value}, cur => ({...cur, quiet_hours: value}));
          }}
        />
      </div>
      {on && (
        <div className="quiet-times" role="group" aria-labelledby="quiet-label">
          <label className="field field-inline-time">
            <span>{t('settings.quietStart')}</span>
            <input
              type="time"
              value={draft.start}
              onChange={e => setDraft(d => ({...d, start: e.target.value}))}
              onBlur={() => commit(draft)}
              aria-invalid={!!error || undefined}
            />
          </label>
          <span aria-hidden="true">–</span>
          <label className="field field-inline-time">
            <span>{t('settings.quietEnd')}</span>
            <input
              type="time"
              value={draft.end}
              onChange={e => setDraft(d => ({...d, end: e.target.value}))}
              onBlur={() => commit(draft)}
              aria-invalid={!!error || undefined}
            />
          </label>
        </div>
      )}
      {error && (
        <span className="field-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

function PrivacySection({s}: {s: UserSettings}) {
  const t = useT();
  const {openPrefs, choice} = useConsent();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState('');
  const [error, setError] = useState('');

  const clear = async () => {
    setBusy(true);
    setError('');
    setResult('');
    try {
      const r = await clearWatchHistory();
      setResult(t('settings.cleared', {n: r?.deleted ?? 0}));
      setConfirming(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('settings.clearFailed'));
    } finally {
      setBusy(false);
    }
  };

  const consentSummary = choice
    ? [
        t('settings.consentNecessary'),
        choice.analytics && t('settings.consentAnalytics'),
        choice.personalized_ads && t('settings.consentAds'),
        choice.personalized_recs && t('settings.consentRecs'),
      ]
        .filter(Boolean)
        .join(' · ')
    : t('settings.consentNone');

  return (
    <>
      <Group title={t('settings.historyGroup')}>
        <BoolRow k="history_paused" label={t('settings.recordHistory')} description={t('settings.recordHistoryDesc')} s={s} invert />
        <div className="set-row set-row-stack">
          <div className="set-row-inline">
            <div className="set-text">
              <span className="set-label">{t('settings.clearHistory')}</span>
              <span className="set-desc">{t('settings.clearHistoryDesc')}</span>
            </div>
            {!confirming && (
              <button type="button" className="btn btn-secondary btn-danger" onClick={() => setConfirming(true)}>
                {t('settings.clear')}
              </button>
            )}
          </div>
          {confirming && (
            <div className="confirm-row" role="group" aria-label={t('settings.confirmClearLabel')}>
              <span>{t('settings.confirmClear')}</span>
              <button type="button" className="btn btn-secondary" onClick={() => setConfirming(false)} disabled={busy}>
                {t('settings.cancel')}
              </button>
              <button type="button" className="btn btn-primary btn-danger-solid" onClick={clear} disabled={busy}>
                {busy ? t('settings.clearing') : t('settings.confirmClearButton')}
              </button>
            </div>
          )}
          <span className="sr-status" role="status">
            {result}
          </span>
          {error && (
            <span className="field-error" role="alert">
              {error}
            </span>
          )}
        </div>
      </Group>
      <Group title={t('settings.dataGroup')}>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.cookies')}</span>
            <span className="set-desc">{consentSummary}</span>
          </div>
          <button type="button" className="btn btn-secondary" onClick={openPrefs}>
            {t('settings.manage')}
          </button>
        </div>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">{t('settings.privacyPolicy')}</span>
            <span className="set-desc">{t('settings.privacyPolicyDesc')}</span>
          </div>
          <Link to="/legal/privacy" className="btn btn-secondary">
            {t('settings.view')}
          </Link>
        </div>
      </Group>
    </>
  );
}
