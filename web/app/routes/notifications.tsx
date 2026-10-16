import {NotificationList} from '../components/Notifications';
import {EmptyState} from '../components/States';
import {useAuth} from '../lib/auth';

export const meta = () => [{title: 'REEL · 通知'}];

/** Full-page Notifications (the TopBar panel's 查看全部通知; phones use this page). */
export default function NotificationsPage() {
  const {status} = useAuth();
  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="bell" title="登录后查看通知" body="关注演员或预约作品后，上线时会在这里提醒你。" actions={[{to: '/login?next=%2Fnotifications', label: '登录'}]} />
      </div>
    );
  }
  return (
    <div className="page narrow">
      <h1 className="page-title">通知</h1>
      {status === 'user' && <NotificationList />}
    </div>
  );
}
