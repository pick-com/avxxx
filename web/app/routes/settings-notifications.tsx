import {Navigate, useLocation} from 'react-router';
import {t} from '../lib/i18n';

export const meta = () => [{title: t('settings.pageTitle')}];

// /settings/notifications (internal/notify.SettingsPath, linked from every
// notification email) opens Settings › 通知. Signed-out visitors get the
// settings sign-in prompt, which returns here after login.
export default function SettingsNotifications() {
  const {hash} = useLocation();
  return <Navigate to={{pathname: '/settings', search: '?s=notify', hash}} replace />;
}
