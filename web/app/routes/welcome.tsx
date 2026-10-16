import {useEffect, useState} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {Avatar} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, ErrorBanner} from '../components/States';
import {Switch} from '../components/Switch';
import {listPerformerPage, listTags, type LangCode, type NotifyMode} from '../lib/api';
import {useAuth} from '../lib/auth';
import {useT} from '../lib/i18n';
import {usePush, type PushState} from '../lib/push';
import {pushStatusText} from '../components/PushOptIn';
import {formatCount} from '../lib/format';
import {useSettings} from '../lib/settings';
import {useSocial} from '../lib/social';
import {themeLabels, useTheme, type ThemePref} from '../lib/theme';
import {useAsync} from '../lib/useAsync';
import {useMediaQuery} from '../lib/useMediaQuery';
import {LANGS, LANG_LABELS} from '../lib/langs';

export const meta = () => [{title: 'REEL · 欢迎'}];

// Onboarding / MobileOnboarding, shown once after registration (login.tsx
// sends new accounts here). Three skippable steps:
//   1. 语言与外观 → PATCH /api/me/settings (ui_lang, content_lang, theme).
//      Phones replace it with 允许新作推送 (notify_prefs.*.push).
//   2. 选 3 个以上类型 → kept in this browser only.
//      TODO(tech plan 3.13 user_interests): save interest tags server-side and
//      use them to suggest performers.
//   3. 关注演员 → PUT /api/me/follows/{slug}; suggestions are the performers
//      with the most works (GET /api/performers?sort=works).

const MIN_TAGS = 3;
const TAGS_KEY = 'interest_tags';
const DONE_KEY = 'onboarding_done';

function safeNext(value: string | null): string {
  return value && value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/login') && !value.startsWith('/welcome') ? value : '/';
}

export default function Welcome() {
  const {status} = useAuth();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const mobile = useMediaQuery('(max-width: 767px)');
  const push = usePush();
  const [step, setStep] = useState(0);
  const next = safeNext(params.get('next'));

  useEffect(() => {
    if (status === 'anon') navigate(`/login?next=${encodeURIComponent('/welcome')}`, {replace: true});
  }, [status, navigate]);

  const finish = () => {
    try {
      window.localStorage.setItem(DONE_KEY, new Date().toISOString());
    } catch {
      /* shown once per registration anyway */
    }
    navigate(next, {replace: true});
  };

  // Phones ask for push first, unless push is not configured on this server.
  const steps = [
    mobile && push.availability !== 'disabled'
      ? {title: '有新作时通知你？', sub: '关注的演员有新作、预约的作品上线时提醒你。可以随时在设置里修改。', body: <PushStep push={push} />}
      : {title: '选择语言和外观', sub: '界面、内容语言和外观分开设置，之后可以在「设置」里修改。', body: <LanguageStep />},
    {title: '你喜欢看哪些类型？', sub: `选 ${MIN_TAGS} 个以上，首页会更懂你。选择只用于推荐，不公开。`, body: null},
    {title: '关注几位演员', sub: '关注后，有新作上线时会提醒你。', body: null},
  ];
  const [tags, setTags] = useState<string[]>([]);
  const current = steps[step];
  const last = step === steps.length - 1;
  const canNext = step !== 1 || tags.length >= MIN_TAGS;

  const goNext = () => {
    if (step === 1) {
      try {
        window.localStorage.setItem(TAGS_KEY, JSON.stringify(tags));
      } catch {
        /* not kept */
      }
    }
    if (last) finish();
    else setStep(s => s + 1);
  };

  return (
    <div className="onboard-page">
      <header className="onboard-top">
        <Link to="/" className="logo" aria-label="REEL 首页">
          REEL<span className="logo-dot">.</span>
        </Link>
        <span className="section-spacer" />
        <button type="button" className="text-btn" onClick={finish}>
          跳过引导
        </button>
      </header>
      <main className="onboard-main" id="main">
        <div className="onboard-progress" aria-hidden="true">
          {steps.map((_, i) => (
            <span key={i} className={i <= step ? 'is-on' : ''} />
          ))}
        </div>
        <p className="eyebrow">
          第 {step + 1} 步，共 {steps.length} 步
        </p>
        <h1 className="onboard-title">{current.title}</h1>
        <p className="muted">{current.sub}</p>
        <div className="onboard-body">
          {step === 0 && current.body}
          {step === 1 && <TagStep picked={tags} onChange={setTags} />}
          {step === 2 && <FollowStep />}
        </div>
      </main>
      <footer className="onboard-foot">
        {step > 0 ? (
          <button type="button" className="btn btn-secondary" onClick={() => setStep(s => s - 1)}>
            上一步
          </button>
        ) : (
          <span />
        )}
        <span className="section-spacer" />
        {step === 1 && tags.length < MIN_TAGS && <span className="muted onboard-hint">已选 {tags.length} / {MIN_TAGS}</span>}
        <button type="button" className="btn btn-secondary" onClick={() => (last ? finish() : setStep(s => s + 1))}>
          跳过
        </button>
        <button type="button" className="btn btn-primary" onClick={goNext} disabled={!canNext}>
          {last ? '完成，开始看' : '下一步'}
        </button>
      </footer>
    </div>
  );
}

function LanguageStep() {
  const {settings, save, status, fieldErrors} = useSettings();
  const {pref, setPref} = useTheme();
  const setLang = (key: 'ui_lang' | 'content_lang', value: LangCode) => save(key, {[key]: value}, cur => ({...cur, [key]: value}));
  return (
    <div className="onboard-form">
      {status === 'ready' && settings && (
        <>
          <label className="field">
            <span>界面语言（目前只有简体中文，其他语言陆续上线）</span>
            <select className="set-select" value={settings.ui_lang} onChange={e => setLang('ui_lang', e.target.value as LangCode)}>
              {LANGS.map(l => (
                <option key={l} value={l}>
                  {LANG_LABELS[l]}
                </option>
              ))}
            </select>
            {fieldErrors.ui_lang && <span className="field-error">{fieldErrors.ui_lang}</span>}
          </label>
          <label className="field">
            <span>内容语言（标题、简介、演员名、标签）</span>
            <select className="set-select" value={settings.content_lang} onChange={e => setLang('content_lang', e.target.value as LangCode)}>
              {LANGS.map(l => (
                <option key={l} value={l}>
                  {LANG_LABELS[l]}
                </option>
              ))}
            </select>
            {fieldErrors.content_lang && <span className="field-error">{fieldErrors.content_lang}</span>}
          </label>
        </>
      )}
      <div className="field">
        <span id="onboard-theme">外观</span>
        <div className="segmented" role="radiogroup" aria-labelledby="onboard-theme">
          {(['system', 'dark', 'light'] as ThemePref[]).map(p => (
            <button key={p} type="button" role="radio" aria-checked={pref === p} className="segmented-item" onClick={() => setPref(p, 'user')}>
              {themeLabels[p]}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

function PushStep({push}: {push: PushState}) {
  const t = useT();
  const {settings, save, status, fieldErrors} = useSettings();
  if (status !== 'ready' || !settings) return <p className="muted">{t('push.laterHint')}</p>;
  const on = settings.notify_prefs.new_work.push && settings.notify_prefs.reservation.push && (push.subscribed || push.availability !== 'available');
  const set = async (v: boolean) => {
    // The permission prompt comes from this tap (never on page load).
    if (v && push.availability === 'available' && !push.subscribed && !(await push.enable())) return;
    save('notify_prefs.push', {notify_prefs: {new_work: {push: v}, reservation: {push: v}}}, cur => ({
      ...cur,
      notify_prefs: {...cur.notify_prefs, new_work: {...cur.notify_prefs.new_work, push: v}, reservation: {...cur.notify_prefs.reservation, push: v}},
    }));
  };
  const deviceNote = push.availability === 'available' && !push.error && push.permission !== 'denied' ? '' : pushStatusText(push, t);
  return (
    <div className="set-card">
      <div className="set-row">
        <div className="set-text">
          <span className="set-label">{t('push.allowNew')}</span>
          <span className="set-desc">{t('push.allowNewDesc')}</span>
          {deviceNote && (
            <span className={push.error ? 'field-error' : 'set-desc'} role={push.error ? 'alert' : undefined}>
              {deviceNote}
            </span>
          )}
          {fieldErrors['notify_prefs.push'] && <span className="field-error">{fieldErrors['notify_prefs.push']}</span>}
        </div>
        <Switch checked={on} busy={push.busy} disabled={push.busy} onChange={v => void set(v)} label={t('push.allowNew')} />
      </div>
    </div>
  );
}

function TagStep({picked, onChange}: {picked: string[]; onChange: (tags: string[]) => void}) {
  const tags = useAsync(signal => listTags(40, signal), []);
  if (tags.error) return <ErrorBanner error={tags.error} onRetry={tags.reload} />;
  if (tags.loading) return tags.slow ? <CardGridSkeleton count={4} variant="row" /> : null;
  const list = (tags.data ?? []).filter(t => t.slug && t.name);
  if (list.length === 0) return <p className="muted">暂时没有可选的类型，可以直接下一步。</p>;
  return (
    <div className="chip-row chip-row-wrap onboard-tags" role="group" aria-label="类型">
      {list.map(t => {
        const on = picked.includes(t.slug);
        return (
          <button
            key={t.slug}
            type="button"
            className={on ? 'chip chip-on chip-lg' : 'chip chip-lg'}
            aria-pressed={on}
            onClick={() => onChange(on ? picked.filter(s => s !== t.slug) : [...picked, t.slug])}
          >
            {on && <Icon name="check" size={14} />}
            {t.name}
          </button>
        );
      })}
    </div>
  );
}

function FollowStep() {
  const {followMode, setFollow, available} = useSocial();
  const [notify, setNotify] = useState(true);
  const [error, setError] = useState('');
  const performers = useAsync(signal => listPerformerPage('works', 12, null, undefined, signal), []);

  if (available.follows === 'no') return <p className="muted">关注功能即将上线，可以直接完成。</p>;
  if (performers.error) return <ErrorBanner error={performers.error} onRetry={performers.reload} />;
  if (performers.loading) return performers.slow ? <CardGridSkeleton count={6} variant="avatar" /> : null;
  const list = performers.data?.items ?? [];
  const mode: NotifyMode = notify ? 'all' : 'off';

  const toggle = async (slug: string, name: string) => {
    setError('');
    const on = followMode(slug) !== null;
    setError(await setFollow(slug, on ? null : mode, name));
  };

  return (
    <>
      <div className="onboard-performers">
        {list.map(p => {
          const on = followMode(p.slug) !== null;
          return (
            <button
              key={p.slug}
              type="button"
              className={on ? 'onboard-performer is-on' : 'onboard-performer'}
              aria-pressed={on}
              aria-label={on ? `已关注 ${p.canonical_name}，点击取消` : `关注 ${p.canonical_name}`}
              onClick={() => toggle(p.slug, p.canonical_name)}
            >
              <span className="onboard-avatar">
                <Avatar seed={p.slug} name={p.canonical_name} size={72} />
                {on && (
                  <span className="onboard-check" aria-hidden="true">
                    <Icon name="check" size={14} />
                  </span>
                )}
              </span>
              <span className="performer-chip-name">{p.canonical_name}</span>
              {typeof p.works_count === 'number' && <span className="performer-chip-sub">{formatCount(p.works_count)} 部作品</span>}
            </button>
          );
        })}
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <label className="onboard-notify">
        <input type="checkbox" checked={notify} onChange={e => setNotify(e.target.checked)} />
        <span>关注的演员有新作时提醒我（站内通知，可在设置里改）</span>
      </label>
    </>
  );
}
