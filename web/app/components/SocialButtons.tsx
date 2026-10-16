import {useEffect, useId, useRef, useState} from 'react';
import {track} from '../lib/analytics';
import type {NotifyMode} from '../lib/api';
import {useAuth} from '../lib/auth';
import {t, useT} from '../lib/i18n';
import {useSocial} from '../lib/social';
import {useRequireAuth} from './AuthPrompt';
import {Icon, type IconName} from './Icon';

// NewWorkAlert: 关注 + bell with three modes; 预约 for upcoming works; 稍后观看.
// Signed-out clicks open the in-page sign-in sheet (AuthPrompt).

// Getters, so labels follow the current UI language.
export const notifyModeLabels: Record<NotifyMode, string> = {
  get all() {
    return t('social.notifyAll');
  },
  get lead_only() {
    return t('social.notifyLeadOnly');
  },
  get off() {
    return t('social.notifyOff');
  },
};
const notifyModeHints: Record<NotifyMode, string> = {
  get all() {
    return t('social.notifyAllHint');
  },
  get lead_only() {
    return t('social.notifyLeadOnlyHint');
  },
  get off() {
    return t('social.notifyOffHint');
  },
};
const modeIcon: Record<NotifyMode, IconName> = {all: 'bell', lead_only: 'bellLead', off: 'bellOff'};

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

/** performerId + source feed the follow_actor event (PRD 14); without an id nothing is tracked. */
export function FollowButton({slug, name, performerId, source = 'other', size = 'md', block = false}: {
  slug: string;
  name: string;
  performerId?: number;
  source?: string;
  size?: 'sm' | 'md';
  block?: boolean;
}) {
  const {status} = useAuth();
  const t = useT();
  const {available, followMode, setFollow, followsReady} = useSocial();
  const requireAuth = useRequireAuth();
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
    if (!requireAuth('follow')) return;
    setBusy(true);
    setError('');
    const err = await setFollow(slug, next, name);
    setBusy(false);
    setError(err);
    if (!err && next !== null && !following && performerId) track('follow_actor', {performer_id: performerId, source});
  };

  const sizeClass = size === 'sm' ? ' btn-sm' : '';
  return (
    <div className={block ? 'follow-group follow-group-block' : 'follow-group'} ref={rootRef}>
      <button
        type="button"
        className={(following ? 'btn btn-secondary' : 'btn btn-primary') + sizeClass + ' follow-btn'}
        aria-pressed={following}
        aria-label={following ? t('social.unfollowLabel', {name}) : t('social.followLabel', {name})}
        disabled={busy || (status === 'user' && !followsReady)}
        onClick={() => run(following ? null : 'all')}
      >
        <Icon name={following ? 'check' : 'plus'} size={16} />
        {following ? t('social.following') : t('social.follow')}
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
            aria-label={t('social.bellLabel', {mode: notifyModeLabels[mode]})}
            title={t('social.bellTitle', {mode: notifyModeLabels[mode]})}
            onClick={() => setOpen(o => !o)}
          >
            <Icon name={modeIcon[mode]} size={18} />
          </button>
          {open && (
            <div className="popover bell-menu" id={menuId} role="group" aria-label={t('social.bellMenu', {name})}>
              <span className="popover-title">{t('social.bellMenu', {name})}</span>
              <div role="radiogroup" aria-label={t('social.notifyMode')}>
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
              <span className="popover-note">{t('social.bellNote')}</span>
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
  const t = useT();
  const {available, later, setLater} = useSocial();
  const requireAuth = useRequireAuth();
  const [error, setError] = useState('');
  if (status === 'user' && available.later === 'no') return null;
  // Card overlays only for signed-in users: a login prompt on every card is noise.
  if (variant === 'icon' && status !== 'user') return null;
  const on = status === 'user' && later.has(slug);

  const toggle = async () => {
    if (!requireAuth('later')) return;
    setError('');
    setError(await setLater(slug, !on));
  };

  const label = on ? t('social.laterRemoveLabel', {title}) : t('social.laterAddLabel', {title});
  if (variant === 'icon') {
    return (
      <button type="button" className={on ? 'card-action is-on' : 'card-action'} aria-pressed={on} aria-label={label} title={error || (on ? t('social.laterIn') : t('nav.later'))} onClick={toggle}>
        <Icon name={on ? 'check' : 'later'} size={18} />
      </button>
    );
  }
  if (variant === 'text') {
    return (
      <button type="button" className="text-btn" aria-label={label} onClick={toggle}>
        {on ? t('social.laterOut') : t('nav.later')}
      </button>
    );
  }
  return (
    <>
      <button type="button" className={on ? 'pill-btn is-on' : 'pill-btn'} aria-pressed={on} aria-label={label} onClick={toggle}>
        <Icon name={on ? 'check' : 'later'} size={18} />
        {on ? t('social.laterAdded') : t('nav.later')}
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
  const t = useT();
  const {available, reserved, setReserved, noteReserved} = useSocial();
  const requireAuth = useRequireAuth();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (initial) noteReserved([slug]);
  }, [initial, slug, noteReserved]);

  if (status === 'user' && available.reservations === 'no') return null;
  const on = status === 'user' && reserved.has(slug);

  const toggle = async () => {
    if (!requireAuth('reserve')) return;
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
        aria-label={on ? t('social.unreserveLabel', {title}) : t('social.reserveLabel', {title})}
        disabled={busy}
        onClick={toggle}
      >
        <Icon name={on ? 'check' : 'calendar'} size={16} />
        {on ? t('social.reserved') : t('social.reserve')}
      </button>
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </span>
  );
}
