import {useEffect, useState} from 'react';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {guestHistory, guestResume} from '../lib/guestProgress';
import {useT} from '../lib/i18n';
import {markShown, readLastShown, shouldRemind} from '../lib/syncReminder';
import {useRequireAuth} from './AuthPrompt';
import {Icon} from './Icon';

/**
 * 「登录后观看记录可跨设备同步」 below the player for signed-out viewers
 * (lib/syncReminder: from the 3rd watched video or on a guest resume, once a
 * day). 登录 opens the sign-in sheet; the guest history then merges into the
 * account (lib/guestMerge). Never drawn over the video.
 */
export function SyncReminder({slug}: {slug: string}) {
  const t = useT();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const [show, setShow] = useState(false);

  useEffect(() => {
    setShow(false);
    if (status !== 'anon' || !slug) return;
    const now = Date.now();
    const watched = guestHistory().map(e => e.slug);
    if (!shouldRemind({lastShown: readLastShown(), now, watched, current: slug, resumed: guestResume(slug) > 0})) return;
    markShown(now);
    setShow(true);
    track('auth_prompt_shown', {action: 'sync', method: 'reminder'});
  }, [status, slug]);

  if (!show || status !== 'anon') return null;
  return (
    <div className="sync-reminder" role="note">
      <Icon name="history" size={18} />
      <span className="sync-reminder-text">{t('syncReminder.text')}</span>
      <button type="button" className="btn btn-primary btn-sm" onClick={() => requireAuth('sync')}>
        {t('auth.login')}
      </button>
      <button
        type="button"
        className="icon-btn sync-reminder-close"
        aria-label={t('common.close')}
        onClick={() => {
          setShow(false);
          track('auth_prompt_closed', {action: 'sync', method: 'dismiss'});
        }}
      >
        <Icon name="close" size={18} />
      </button>
    </div>
  );
}
