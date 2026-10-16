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
const SECTIONS: {key: SectionKey; title: string; desc: string; icon: IconName}[] = [
  {key: 'account', title: '账号', desc: '登录方式与账号安全。', icon: 'me'},
  {key: 'playback', title: '播放', desc: '这些设置在所有设备上同步。', icon: 'play'},
  {key: 'language', title: '语言与外观', desc: '界面、内容、字幕三者分开设置。', icon: 'globe'},
  {key: 'notify', title: '通知', desc: '只发你关心的。每类都可以单独关掉。', icon: 'bell'},
  {key: 'privacy', title: '隐私与数据', desc: '你的数据怎么用，由你决定。', icon: 'lock'},
];

const QUALITY_LABELS: Record<QualityPref, string> = {auto: '自动', '2160p': '4K（2160p）', '1080p': '1080p', '720p': '720p', '480p': '480p', '360p': '360p'};
const KIND_LABELS: [NotifyKind, string, string][] = [
  ['new_work', '关注演员的新作', '按每位演员的铃铛设置（所有新作 / 只提醒主演 / 不提醒）'],
  ['reservation', '预约的视频上线', '预约过的作品上线时提醒'],
  ['comment_reply', '评论被回复', ''],
  ['favorite_update', '收藏的视频有更新', '新增片源或字幕'],
];
const CHANNEL_LABELS: [NotifyChannel, string][] = [
  ['in_app', '站内'],
  ['push', '推送'],
  ['email', '邮件'],
];
const DEFAULT_QUIET = {start: '22:00', end: '08:00'};

export default function SettingsPage() {
  const {status: auth} = useAuth();
  const {status, error, reload} = useSettings();
  const [params, setParams] = useSearchParams();
  const mobile = useMediaQuery('(max-width: 767px)');
  const raw = params.get('s') as SectionKey | null;
  const section = SECTIONS.find(s => s.key === raw) ?? null;

  if (auth === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="settings" title="登录后管理设置" body="播放、通知和隐私设置会同步到你的所有设备。外观可以直接用顶栏的按钮切换。" actions={[{to: '/login?next=%2Fsettings', label: '登录'}]} />
      </div>
    );
  }
  if (status === 'missing') {
    return (
      <div className="page">
        <EmptyState icon="settings" title="设置页即将上线" body="当前服务还不支持同步设置。外观可以先用顶栏的按钮切换。" actions={[{to: '/me', label: '返回我的'}]} />
      </div>
    );
  }

  const current = section ?? (mobile ? null : SECTIONS[0]);
  const pick = (key: SectionKey) => setParams({s: key}, {replace: !mobile});

  return (
    <div className="page settings-page">
      <h1 className="page-title">{mobile && current ? current.title : '设置'}</h1>
      {status === 'error' && error && <ErrorBanner error={error} onRetry={reload} />}
      <div className="settings-layout">
        {(!mobile || !current) && (
          <nav className="settings-nav" aria-label="设置分类">
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
                <Icon name="back" size={16} /> 全部设置
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
  const {user, signOut} = useAuth();
  const navigate = useNavigate();
  return (
    <>
      <Group title="基本信息">
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">邮箱</span>
            <span className="set-desc">用于登录和找回</span>
          </div>
          <span className="set-value">{user?.email ?? ''}</span>
        </div>
      </Group>
      <p className="muted">修改昵称和密码、已登录设备、第三方登录、注销账号即将支持。</p>
      <button
        type="button"
        className="btn btn-secondary"
        onClick={() => {
          signOut();
          navigate('/');
        }}
      >
        退出登录
      </button>
    </>
  );
}

function PlaybackSection({s}: {s: UserSettings}) {
  const {save} = useSettings();
  return (
    <>
      <Group title="画质与流量">
        <SelectRow
          id="set-quality"
          label="默认画质"
          description="网络差时自动降低"
          value={s.quality_pref}
          options={(Object.keys(QUALITY_LABELS) as QualityPref[]).map(q => [q, QUALITY_LABELS[q]])}
          errorKey="quality_pref"
          onChange={v => save('quality_pref', {quality_pref: v}, cur => ({...cur, quality_pref: v}))}
        />
        <BoolRow k="data_saver" label="省流量模式" description="用流量时少预加载" s={s} />
      </Group>
      <Group title="习惯">
        <BoolRow k="autoplay" label="自动播放下一个" s={s} />
        <BoolRow k="remember_rate" label="记住播放速度" s={s} />
      </Group>
    </>
  );
}

function LanguageSection({s}: {s: UserSettings}) {
  const {save, fieldErrors} = useSettings();
  const {setPref} = useTheme();
  const langOptions = LANGS.map(l => [l, LANG_LABELS[l]] as [LangCode, string]);
  const toggleSub = (lang: LangCode) => {
    const next = s.subtitle_langs.includes(lang) ? s.subtitle_langs.filter(l => l !== lang) : [...s.subtitle_langs, lang];
    save('subtitle_langs', {subtitle_langs: next}, cur => ({...cur, subtitle_langs: next}));
  };
  return (
    <>
      <Group title="语言">
        <SelectRow
          id="set-ui-lang"
          label="界面语言"
          description="目前界面只有简体中文，其他语言陆续上线"
          value={s.ui_lang}
          options={langOptions}
          errorKey="ui_lang"
          onChange={v => save('ui_lang', {ui_lang: v}, cur => ({...cur, ui_lang: v}))}
        />
        <SelectRow
          id="set-content-lang"
          label="内容语言"
          description="标题、简介、演员名、标签"
          value={s.content_lang}
          options={langOptions}
          errorKey="content_lang"
          onChange={v => save('content_lang', {content_lang: v}, cur => ({...cur, content_lang: v}))}
        />
        <div className="set-row set-row-stack">
          <div className="set-text">
            <span className="set-label" id="sub-langs-label">
              字幕优先顺序
            </span>
            <span className="set-desc">
              {s.subtitle_langs.length > 0 ? s.subtitle_langs.map(l => LANG_LABELS[l] ?? l).join(' → ') : '未设置：按片源默认'}。第一个没有时用下一个，按点选顺序排列。
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
      <Group title="外观">
        <div className="set-row set-row-stack">
          <div className="set-text">
            <span className="set-label" id="theme-label">
              主题
            </span>
            <span className="set-desc">和顶栏的外观按钮同步</span>
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
  const {save, pending} = useSettings();
  const tz = browserTimezone();
  const setChannel = (kind: NotifyKind, ch: NotifyChannel, on: boolean) =>
    save(`notify_prefs.${kind}`, {notify_prefs: {[kind]: {[ch]: on}}}, cur => ({
      ...cur,
      notify_prefs: {...cur.notify_prefs, [kind]: {...cur.notify_prefs[kind], [ch]: on}},
    }));

  return (
    <>
      <Group title="提醒方式">
        {KIND_LABELS.map(([kind, label, desc]) => (
          <div className="set-row set-row-stack" key={kind}>
            <div className="set-text">
              <span className="set-label" id={`kind-${kind}`}>
                {label}
              </span>
              {desc && <span className="set-desc">{desc}</span>}
              <FieldError k={`notify_prefs.${kind}`} />
            </div>
            <div className="chip-row chip-row-wrap" role="group" aria-labelledby={`kind-${kind}`}>
              {CHANNEL_LABELS.map(([ch, chLabel]) => {
                const on = s.notify_prefs?.[kind]?.[ch] ?? false;
                return (
                  <button
                    key={ch}
                    type="button"
                    className={on ? 'chip chip-lg chip-on' : 'chip chip-lg'}
                    aria-pressed={on}
                    aria-label={`${label}：${chLabel}`}
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
        ))}
        <p className="set-note">推送需要在设备上授权。网页推送即将支持，开关会先保存到你的账号。</p>
      </Group>
      <Group title="时机与频率">
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">什么时候提醒</span>
            <span className="set-desc">作品上线时提醒；「上线前一天」提醒即将支持</span>
          </div>
          <span className="set-value">上线时</span>
        </div>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">每天最多</span>
            <span className="set-desc">每个渠道每天最多 1 条，超出的合并成一条</span>
          </div>
          <span className="set-value">1 条</span>
        </div>
        <QuietHours s={s} />
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">时区</span>
            <span className="set-desc">免打扰时段和每天合并按这个时区计算</span>
            <FieldError k="timezone" />
          </div>
          <span className="set-value">
            {s.timezone}
            {tz !== s.timezone && (
              <button type="button" className="text-btn" onClick={() => save('timezone', {timezone: tz}, cur => ({...cur, timezone: tz}))}>
                改为本机时区（{tz}）
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
      setLocal('请输入 HH:MM 格式的时间');
      return;
    }
    if (next.start === next.end) {
      setLocal('开始和结束时间不能相同');
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
            免打扰时段
          </span>
          <span className="set-desc">这段时间的推送和邮件顺延到结束时发，站内通知照常</span>
        </div>
        <Switch
          checked={on}
          label="免打扰时段"
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
            <span>开始</span>
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
            <span>结束</span>
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
      setResult(`已清除 ${r?.deleted ?? 0} 条观看记录`);
      setConfirming(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '清除失败');
    } finally {
      setBusy(false);
    }
  };

  const consentSummary = choice
    ? ['必要', choice.analytics && '分析', choice.personalized_ads && '个性化广告', choice.personalized_recs && '个性化推荐'].filter(Boolean).join(' · ')
    : '尚未选择（只用必要的）';

  return (
    <>
      <Group title="观看记录">
        <BoolRow k="history_paused" label="记录观看历史" description="关闭后不再记录新的观看进度，已有记录保留" s={s} invert />
        <div className="set-row set-row-stack">
          <div className="set-row-inline">
            <div className="set-text">
              <span className="set-label">清除观看记录</span>
              <span className="set-desc">删除全部观看记录和进度，无法恢复</span>
            </div>
            {!confirming && (
              <button type="button" className="btn btn-secondary btn-danger" onClick={() => setConfirming(true)}>
                清除
              </button>
            )}
          </div>
          {confirming && (
            <div className="confirm-row" role="group" aria-label="确认清除观看记录">
              <span>确定清除全部观看记录吗？</span>
              <button type="button" className="btn btn-secondary" onClick={() => setConfirming(false)} disabled={busy}>
                取消
              </button>
              <button type="button" className="btn btn-primary btn-danger-solid" onClick={clear} disabled={busy}>
                {busy ? '正在清除…' : '确定清除'}
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
      <Group title="数据">
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">Cookie 与追踪</span>
            <span className="set-desc">{consentSummary}</span>
          </div>
          <button type="button" className="btn btn-secondary" onClick={openPrefs}>
            管理
          </button>
        </div>
        <div className="set-row">
          <div className="set-text">
            <span className="set-label">隐私政策</span>
            <span className="set-desc">收集哪些信息、怎么使用、你的权利</span>
          </div>
          <Link to="/legal/privacy" className="btn btn-secondary">
            查看
          </Link>
        </div>
      </Group>
    </>
  );
}
