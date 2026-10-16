import {useEffect, useId, useRef, useState} from 'react';
import {useLocation, useNavigate} from 'react-router';
import type {NotifyMode} from '../lib/api';
import {useAuth} from '../lib/auth';
import {useSocial} from '../lib/social';
import {Icon, type IconName} from './Icon';

// NewWorkAlert: 关注 + bell with three modes; 预约 for upcoming works; 稍后观看.
// Signed-out clicks go to login and come back to the same page.

export const notifyModeLabels: Record<NotifyMode, string> = {all: '所有新作', lead_only: '只提醒主演作品', off: '不提醒'};
const notifyModeHints: Record<NotifyMode, string> = {
  all: '有新作上线就提醒',
  lead_only: '只在担任主演时提醒',
  off: '仍然关注，但不发提醒',
};
const modeIcon: Record<NotifyMode, IconName> = {all: 'bell', lead_only: 'bellLead', off: 'bellOff'};

function useLoginRedirect() {
  const navigate = useNavigate();
  const location = useLocation();
  return () => navigate(`/login?next=${encodeURIComponent(location.pathname + location.search)}`);
}

/** Closes on outside pointer-down and Esc; returns focus to the trigger on Esc. */
export function useDismiss(open: boolean, close: () => void, rootRef: React.RefObject<HTMLElement | null>, triggerRef?: React.RefObject<HTMLElement | null>) {
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    if (!open) return;
    const onDown = (event: PointerEvent) => {
      if (rootRef.current && event.target instanceof Node && !rootRef.current.contains(event.target)) closeRef.current();
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      closeRef.current();
      triggerRef?.current?.focus();
    };
    document.addEventListener('pointerdown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open, rootRef, triggerRef]);
}

export function FollowButton({slug, name, size = 'md', block = false}: {slug: string; name: string; size?: 'sm' | 'md'; block?: boolean}) {
  const {status} = useAuth();
  const {available, followMode, setFollow, followsReady} = useSocial();
  const toLogin = useLoginRedirect();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const bellRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();
  useDismiss(open, () => setOpen(false), rootRef, bellRef);

  if (status === 'user' && available.follows === 'no') return null;
  const mode = status === 'user' ? followMode(slug) : null;
  const following = mode !== null;

  const run = async (next: NotifyMode | null) => {
    if (status !== 'user') {
      toLogin();
      return;
    }
    setBusy(true);
    setError('');
    const err = await setFollow(slug, next, name);
    setBusy(false);
    setError(err);
  };

  const sizeClass = size === 'sm' ? ' btn-sm' : '';
  return (
    <div className={block ? 'follow-group follow-group-block' : 'follow-group'} ref={rootRef}>
      <button
        type="button"
        className={(following ? 'btn btn-secondary' : 'btn btn-primary') + sizeClass + ' follow-btn'}
        aria-pressed={following}
        aria-label={following ? `已关注 ${name}，点击取消关注` : `关注 ${name}`}
        disabled={busy || (status === 'user' && !followsReady)}
        onClick={() => run(following ? null : 'all')}
      >
        <Icon name={following ? 'check' : 'plus'} size={16} />
        {following ? '已关注' : '关注'}
      </button>
      {following && mode && (
        <>
          <button
            ref={bellRef}
            type="button"
            className={'btn btn-secondary bell-btn' + sizeClass}
            aria-haspopup="true"
            aria-expanded={open}
            aria-controls={open ? menuId : undefined}
            aria-label={`新作提醒：${notifyModeLabels[mode]}，点击修改`}
            title={`新作提醒：${notifyModeLabels[mode]}`}
            onClick={() => setOpen(o => !o)}
          >
            <Icon name={modeIcon[mode]} size={18} />
          </button>
          {open && (
            <div className="popover bell-menu" id={menuId} role="group" aria-label={`${name}有新作时提醒我`}>
              <span className="popover-title">{name}有新作时提醒我</span>
              <div role="radiogroup" aria-label="提醒方式">
                {(Object.keys(notifyModeLabels) as NotifyMode[]).map(m => (
                  <button
                    key={m}
                    type="button"
                    role="radio"
                    aria-checked={mode === m}
                    className="menu-choice"
                    onClick={async () => {
                      setOpen(false);
                      bellRef.current?.focus();
                      if (m !== mode) await run(m);
                    }}
                  >
                    <Icon name={modeIcon[m]} size={18} />
                    <span className="menu-choice-text">
                      <span>{notifyModeLabels[m]}</span>
                      <span className="menu-choice-hint">{notifyModeHints[m]}</span>
                    </span>
                    {mode === m && <Icon name="check" size={16} />}
                  </button>
                ))}
              </div>
              <span className="popover-note">渠道和免打扰时段在「设置 › 通知」统一管理</span>
            </div>
          )}
        </>
      )}
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

/** 稍后观看 toggle. `variant="icon"` overlays a card thumbnail. */
export function LaterButton({slug, title, variant = 'pill'}: {slug: string; title: string; variant?: 'pill' | 'icon' | 'text'}) {
  const {status} = useAuth();
  const {available, later, setLater} = useSocial();
  const toLogin = useLoginRedirect();
  const [error, setError] = useState('');
  if (status === 'user' && available.later === 'no') return null;
  // Card overlays only for signed-in users: a login prompt on every card is noise.
  if (variant === 'icon' && status !== 'user') return null;
  const on = status === 'user' && later.has(slug);

  const toggle = async () => {
    if (status !== 'user') {
      toLogin();
      return;
    }
    setError('');
    setError(await setLater(slug, !on));
  };

  const label = on ? `从稍后观看移除《${title}》` : `加入稍后观看：《${title}》`;
  if (variant === 'icon') {
    return (
      <button type="button" className={on ? 'card-action is-on' : 'card-action'} aria-pressed={on} aria-label={label} title={error || (on ? '已在稍后观看' : '稍后观看')} onClick={toggle}>
        <Icon name={on ? 'check' : 'later'} size={18} />
      </button>
    );
  }
  if (variant === 'text') {
    return (
      <button type="button" className="text-btn" aria-label={label} onClick={toggle}>
        {on ? '移出稍后观看' : '稍后观看'}
      </button>
    );
  }
  return (
    <>
      <button type="button" className={on ? 'pill-btn is-on' : 'pill-btn'} aria-pressed={on} aria-label={label} onClick={toggle}>
        <Icon name={on ? 'check' : 'later'} size={18} />
        {on ? '已加入稍后观看' : '稍后观看'}
      </button>
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </>
  );
}

/** 预约 / 已预约 for an upcoming release (PRD 17: no follow needed). */
export function ReserveButton({slug, title, initial, size = 'md'}: {slug: string; title: string; initial?: boolean; size?: 'sm' | 'md'}) {
  const {status} = useAuth();
  const {available, reserved, setReserved, noteReserved} = useSocial();
  const toLogin = useLoginRedirect();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (initial) noteReserved([slug]);
  }, [initial, slug, noteReserved]);

  if (status === 'user' && available.reservations === 'no') return null;
  const on = status === 'user' && reserved.has(slug);

  const toggle = async () => {
    if (status !== 'user') {
      toLogin();
      return;
    }
    setBusy(true);
    setError('');
    setError(await setReserved(slug, !on));
    setBusy(false);
  };

  return (
    <span className="reserve-wrap">
      <button
        type="button"
        className={(on ? 'btn btn-secondary' : 'btn btn-outline-accent') + (size === 'sm' ? ' btn-sm' : '')}
        aria-pressed={on}
        aria-label={on ? `已预约《${title}》，点击取消预约` : `预约《${title}》，上线时提醒我`}
        disabled={busy}
        onClick={toggle}
      >
        <Icon name={on ? 'check' : 'calendar'} size={16} />
        {on ? '已预约' : '预约'}
      </button>
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </span>
  );
}
