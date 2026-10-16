import {redirect, type LoaderFunctionArgs} from 'react-router';
import {DEFAULT_LANG, isLang, localizePath} from '../lib/langs';

// /settings/notifications (internal/notify.SettingsPath, linked from every
// notification email) opens Settings › 通知. Signed-out visitors get the
// settings sign-in prompt, which returns here after login.
function target({params}: LoaderFunctionArgs) {
  const lang = isLang(params.lang) ? params.lang : DEFAULT_LANG;
  return redirect(localizePath('/settings?s=notify', lang));
}

export const loader = (args: LoaderFunctionArgs) => target(args);
export const clientLoader = (args: LoaderFunctionArgs) => target(args);

export default function SettingsNotifications() {
  return null;
}
