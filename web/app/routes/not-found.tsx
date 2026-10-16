import {EmptyState} from '../components/States';
import {t, useT} from '../lib/i18n';

export const meta = () => [{title: t('title.notFound')}];

export default function NotFound() {
  const t = useT();
  return (
    <div className="page">
      <EmptyState title={t('error.notFound')} body={t('error.notFoundBody')} actions={[{to: '/', label: t('error.backHome')}, {to: '/search', label: t('topbar.search'), primary: false}]} />
    </div>
  );
}
