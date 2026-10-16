import {Link} from 'react-router';
import {SectionHeader} from './Cards';
import type {ReleaseVersionRef} from '../lib/api';
import {formatDate, formatRuntime} from '../lib/format';
import {useT} from '../lib/i18n';
import '../styles/versions.css';

// 其他版本: published releases an editor linked as versions of the same work
// (AdminDedupe, migration 047 release_versions), from GET /api/releases/{slug}
// other_versions. Hidden when there are none.
export function OtherVersions({items}: {items?: ReleaseVersionRef[] | null}) {
  const t = useT();
  const list = (items ?? []).filter(v => v?.slug);
  if (list.length === 0) return null;
  return (
    <section className="other-versions" aria-label={t('release.otherVersions')}>
      <SectionHeader title={t('release.otherVersions')} />
      <ul className="other-versions-list">
        {list.map(v => (
          <li key={v.id}>
            <Link to={`/releases/${encodeURIComponent(v.slug)}`} className="meta-link">
              {v.title}
            </Link>
            <span className="other-versions-meta">
              {[v.code, formatDate(v.release_date), formatRuntime(v.runtime_minutes)].filter(Boolean).join(' · ')}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
