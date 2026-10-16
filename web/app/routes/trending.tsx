import {useState} from 'react';
import {useSearchParams} from 'react-router';
import {EmptyState} from '../components/States';
import {parsePeriod, PeriodTabs, TrendingBoard} from '../components/Trending';
import {t, useT} from '../lib/i18n';

export const meta = () => [{title: t('title.trending')}];

// NavTrending. ?period=today|week|month|year (default week). Without the
// trending route (backend before the recs PR) the page says so and points to
// 新上线 instead of showing an error.
export default function TrendingPage() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const period = parsePeriod(params.get('period'));
  const [missing, setMissing] = useState(false);

  return (
    <div className="page trending-page">
      <div className="trending-head">
        <h1 className="page-title">{t('trending.title')}</h1>
        <PeriodTabs period={period} onChange={p => setParams(p === 'week' ? {} : {period: p}, {replace: true})} />
        <span className="section-spacer" />
        <span className="section-sub hide-mobile">{t('trending.updated')}</span>
      </div>
      {missing ? (
        <EmptyState icon="trending" title={t('trending.empty')} body={t('trending.emptyBody')} actions={[{to: '/discover?tab=new', label: t('nav.new')}]} />
      ) : (
        <TrendingBoard period={period} onMissing={() => setMissing(true)} />
      )}
    </div>
  );
}
