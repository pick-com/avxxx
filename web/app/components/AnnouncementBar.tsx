import {useEffect, useState} from 'react';
import {Link} from '../lib/nav';
import {useI18n, useT} from '../lib/i18n';
import {
  dismissAnnouncementRemote,
  dismissalKey,
  getAnnouncements,
  getDismissedAnnouncements,
  readDismissals,
  signedIn,
  storeDismissal,
  type Announcement,
} from '../lib/home';
import {useAsync} from '../lib/useAsync';
import {Icon} from './Icon';
import '../styles/home-ops.css';

/**
 * Site announcements under the top bar (PRD 11 AdminOps 公告条; GET
 * /api/announcements). The shell loader renders them on the server for the
 * page language; dismissals are remembered per visitor in localStorage
 * (id:version, so an edited announcement shows again) and, signed in, on the
 * server (/api/me/announcement-dismissals) so they follow the account.
 */
export function AnnouncementBar({initial}: {initial?: Announcement[]}) {
  const t = useT();
  const {uiLang} = useI18n();
  const anns = useAsync(signal => getAnnouncements(uiLang, signal), [uiLang], true, initial);
  // Hidden ids are applied after hydration (storage is browser-only), so the
  // first client render matches the server HTML.
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());

  useEffect(() => {
    const local = readDismissals();
    setHidden(new Set(local));
    if (!signedIn()) return;
    const ctrl = new AbortController();
    getDismissedAnnouncements(ctrl.signal).then(
      ids => setHidden(prev => new Set([...prev, ...ids.map(id => `id:${id}`)])),
      () => {
        /* signed-out token or older backend: localStorage only */
      },
    );
    return () => ctrl.abort();
  }, []);

  const items = (anns.data ?? []).filter(a => !(a.dismissible && (hidden.has(dismissalKey(a)) || hidden.has(`id:${a.id}`))));
  if (items.length === 0) return null;

  const dismiss = (a: Announcement) => {
    storeDismissal(a);
    setHidden(prev => new Set([...prev, dismissalKey(a)]));
    if (signedIn()) dismissAnnouncementRemote(a.id).catch(() => {});
  };

  return (
    <div className="ann-slot" role="region" aria-label={t('shell.announcements')}>
      {items.map(a => (
        <div key={a.id} className={`ann-bar banner banner-${a.severity}`} role={a.severity === 'critical' ? 'alert' : 'status'}>
          <Icon name={a.severity === 'info' ? 'bell' : 'alert'} size={18} />
          <span className="banner-text">
            {a.body}
            {a.link_url && (
              <>
                {' '}
                {a.external ? (
                  <a className="ann-link" href={a.link_url} target="_blank" rel="noopener noreferrer">
                    {a.link_label || t('shell.announcementMore')}
                  </a>
                ) : (
                  <Link className="ann-link" to={a.link_url}>
                    {a.link_label || t('shell.announcementMore')}
                  </Link>
                )}
              </>
            )}
          </span>
          {a.dismissible && (
            <button type="button" className="ann-close" onClick={() => dismiss(a)} aria-label={t('shell.announcementDismiss')}>
              <Icon name="close" size={16} />
            </button>
          )}
        </div>
      ))}
    </div>
  );
}
