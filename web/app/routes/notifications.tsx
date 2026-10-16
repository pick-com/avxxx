import {NotificationList} from '../components/Notifications';
import {EmptyState} from '../components/States';
import {useAuth} from '../lib/auth';
import {t, useT} from '../lib/i18n';

export const meta = () => [{title: t('title.notifications')}];

/** Full-page Notifications (the TopBar panel's 查看全部通知; phones use this page). */
export default function NotificationsPage() {
  const t = useT();
  const {status} = useAuth();
  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="bell" title={t('notif.anonTitle')} body={t('notif.anonBody')} actions={[{to: '/login?next=%2Fnotifications', label: t('auth.login')}]} />
      </div>
    );
  }
  return (
    <div className="page narrow">
      <h1 className="page-title">{t('notif.title')}</h1>
      {status === 'user' && <NotificationList />}
    </div>
  );
}
