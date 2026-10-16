import type {ReactNode} from 'react';
import {listReleasePage, needsFilterSupport, type Page, type Release, type ReleaseFilter} from '../lib/api';
import {formatDate} from '../lib/format';
import {t as translate, useT} from '../lib/i18n';
import {DURATION_BUCKETS, bucketFor, durationLabel, yearOptions} from '../lib/releaseFilter';
import {usePaged} from '../lib/usePaged';
import {ReleaseCard} from './Cards';
import {Icon} from './Icon';
import {LoadMore} from './Paging';
import {CardGridSkeleton, EmptyState, ErrorBanner} from './States';

type Patch = Partial<Record<keyof ReleaseFilter, string | number | undefined>>;
export type SortOption = {value: string; label: string};

/**
 * Browse filter row: active facet chips (removable), 时长 / 年份 selects and a
 * sort switch. Only facets GET /api/releases supports are offered.
 */
export function FilterBar({filter, onChange, tagName, sortOptions, sortValue, onSort, children}: {
  filter: ReleaseFilter;
  onChange: (patch: Patch) => void;
  tagName?: string;
  sortOptions: SortOption[];
  sortValue: string;
  onSort: (value: string) => void;
  children?: ReactNode;
}) {
  const t = useT();
  const bucket = bucketFor(filter);
  const customDuration = !bucket && (filter.min_runtime || filter.max_runtime);
  const active: {key: string; label: string; clear: Patch}[] = [];
  if (filter.tag) active.push({key: 'tag', label: `#${tagName || filter.tag}`, clear: {tag: undefined}});
  if (filter.maker) active.push({key: 'maker', label: t('filter.maker', {name: filter.maker}), clear: {maker: undefined}});
  if (filter.performer) active.push({key: 'performer', label: t('filter.performer', {name: filter.performer}), clear: {performer: undefined}});

  return (
    <div className="filter-bar">
      {active.length > 0 && (
        <div className="chip-row chip-row-wrap" aria-label={t('filter.active')}>
          {active.map(a => (
            <button key={a.key} type="button" className="chip chip-on" aria-label={t('filter.remove', {label: a.label})} onClick={() => onChange(a.clear)}>
              {a.label}
              <Icon name="close" size={14} />
            </button>
          ))}
        </div>
      )}
      <div className="filter-row">
        <label className="filter-select">
          <span className="sr-only">{t('filter.duration')}</span>
          <select
            value={bucket?.key ?? (customDuration ? 'custom' : '')}
            onChange={e => {
              const b = DURATION_BUCKETS.find(x => x.key === e.target.value);
              onChange({min_runtime: b?.min, max_runtime: b?.max});
            }}
          >
            <option value="">{t('filter.durationAll')}</option>
            {customDuration && <option value="custom">{t('filter.durationValue', {label: durationLabel(filter)})}</option>}
            {DURATION_BUCKETS.map(b => (
              <option key={b.key} value={b.key}>
                {t('filter.durationValue', {label: b.label})}
              </option>
            ))}
          </select>
        </label>
        <label className="filter-select">
          <span className="sr-only">{t('filter.year')}</span>
          <select value={filter.year ?? ''} onChange={e => onChange({year: e.target.value ? Number(e.target.value) : undefined})}>
            <option value="">{t('filter.yearAll')}</option>
            {filter.year && !yearOptions().includes(filter.year) && <option value={filter.year}>{t('common.year', {y: filter.year})}</option>}
            {yearOptions().map(y => (
              <option key={y} value={y}>
                {t('common.year', {y})}
              </option>
            ))}
          </select>
        </label>
        {children}
        <span className="section-spacer" />
        <span className="filter-label">{t('common.sort')}</span>
        <div className="segmented segmented-sm" role="radiogroup" aria-label={t('common.sort')}>
          {sortOptions.map(o => (
            <button key={o.value} type="button" role="radio" aria-checked={o.value === sortValue} className="segmented-item" onClick={() => onSort(o.value)}>
              {o.label}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

export const RELEASE_PAGE_SIZE = 24;
const PAGE_SIZE = RELEASE_PAGE_SIZE;

export function releaseCardProps(r: Release) {
  return {
    slug: r.slug,
    title: r.canonical_title,
    badge: r.runtime_minutes ? translate('card.minutes', {n: r.runtime_minutes}) : undefined,
    meta: [r.maker_name, formatDate(r.release_date)].filter(Boolean).join(' · ') || undefined,
    cover: r.cover,
  };
}

/**
 * Filtered, cursor-paged release grid (GET /api/releases). On a backend
 * without filter support (no next_cursor) a filtered request would come back
 * unfiltered, so the results are withheld and the user is told instead.
 */
export function ReleaseResults({filter, onClear, clearLabel, empty, gridClassName = 'card-grid', renderCard, initial}: {
  filter: ReleaseFilter;
  /** First page from the route loader (server rendering), for this filter. */
  initial?: Page<Release>;
  onClear: () => void;
  clearLabel?: string;
  empty: ReactNode;
  gridClassName?: string;
  renderCard?: (r: Release) => ReactNode;
}) {
  const t = useT();
  const key = JSON.stringify(filter);
  const list = usePaged((cursor, signal) => listReleasePage(filter, PAGE_SIZE, cursor, signal), [key], true, initial);
  const unsupported = list.paged === false && needsFilterSupport(filter);

  if (list.error) return <ErrorBanner error={list.error} onRetry={list.reload} />;
  if (list.loading) return list.slow ? <CardGridSkeleton count={10} /> : null;
  if (unsupported) {
    return (
      <EmptyState
        icon="search"
        title={t('filter.soonTitle')}
        body={t('filter.soonBody')}
      >
        <div className="empty-actions">
          <button type="button" className="btn btn-primary" onClick={onClear}>
            {clearLabel ?? t('filter.viewAllNew')}
          </button>
        </div>
      </EmptyState>
    );
  }
  if (list.items.length === 0) return <>{empty}</>;
  return (
    <>
      <div className={gridClassName}>
        {list.items.map(r => (renderCard ? renderCard(r) : <ReleaseCard key={r.id} {...releaseCardProps(r)} />))}
      </div>
      <LoadMore state={list} />
    </>
  );
}
