import {useEffect, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {Avatar, ReleaseCard, SectionHeader} from '../components/Cards';
import {FilterBar, ReleaseResults, type SortOption} from '../components/ReleaseFilters';
import {SearchBox} from '../components/SearchBox';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {catalogSearch, type ReleaseFilter, type SearchHit} from '../lib/api';
import {formatDate, formatYear} from '../lib/format';
import {clearRecentSearches, readRecentSearches, rememberSearch} from '../lib/recentSearches';
import {RELEASE_SORTS, parseReleaseFilter, withFilter} from '../lib/releaseFilter';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 搜索'}];

const LIMIT = 60;
const SORTS: SortOption[] = [{value: 'relevance', label: '相关度'}, ...RELEASE_SORTS];

// Browse / MobileSearch / MobileSearchTyping.
//
// Default (相关度): GET /api/catalog/search — performers first, then releases,
// title match only. Choosing a sort, 时长 or 年份 switches the video results to
// GET /api/releases?q=… (title / product-code match) with cursor paging.
// TODO(tech plan 3.4): 类型 / 清晰度 / 字幕 filters, result counts, alias / tag /
//   cross-language recall, modifier-word retry (PRD 4.5).
// TODO(tech plan 3.13 search_queries): 大家都在搜; log zero-result queries for AdminSearch.
export default function SearchPage() {
  const [params, setParams] = useSearchParams();
  const q = (params.get('q') ?? '').trim();
  const filter = parseReleaseFilter(params);
  const browse = !!(filter.sort || filter.year || filter.min_runtime || filter.max_runtime || filter.tag || filter.maker || filter.performer);
  const results = useAsync(signal => catalogSearch(q, LIMIT, signal), [q], q !== '');
  const [recent, setRecent] = useState<string[]>([]);

  useEffect(() => {
    if (q) rememberSearch(q);
    setRecent(readRecentSearches());
  }, [q]);

  const performers = (results.data ?? []).filter(h => h.type === 'performer');
  const releases = (results.data ?? []).filter(h => h.type === 'release');

  const update = (patch: Partial<Record<keyof ReleaseFilter, string | number | undefined>>) =>
    setParams(withFilter(params, patch), {replace: true});
  const clearFilters = () => setParams({q}, {replace: true});

  return (
    <div className="page search-page">
      <SearchBox initialQuery={q} autoFocus={!q} className="page-search show-mobile" />

      {!q ? (
        <section className="shelf">
          {recent.length > 0 ? (
            <>
              <SectionHeader
                title="最近搜索"
                action={
                  <button
                    type="button"
                    className="text-btn"
                    onClick={() => {
                      clearRecentSearches();
                      setRecent([]);
                    }}
                  >
                    清除
                  </button>
                }
              />
              <div className="chip-row chip-row-wrap">
                {recent.map(term => (
                  <Link key={term} to={`/search?q=${encodeURIComponent(term)}`} className="chip">
                    {term}
                  </Link>
                ))}
              </div>
            </>
          ) : (
            <EmptyState icon="search" title="搜索视频和演员" body="输入作品标题或演员名（中文或日文）。" actions={[{to: '/discover', label: '先逛逛发现页', primary: false}]} />
          )}
        </section>
      ) : (
        <>
          <div className="search-head">
            <h1>搜索：{q}</h1>
            {!browse && results.data && results.data.length > 0 && (
              <span className="muted">{results.data.length >= LIMIT ? `显示前 ${LIMIT} 个结果` : `${results.data.length} 个结果`}</span>
            )}
          </div>

          {performers.length > 0 && (
            <div className="performer-hits">
              {performers.map(hit => (
                <Link key={hit.id} to={`/performers/${encodeURIComponent(hit.slug)}`} className="performer-hit">
                  <Avatar seed={hit.slug} name={hit.name} size={64} />
                  <span className="performer-hit-text">
                    <span className="performer-hit-kind">演员</span>
                    <span className="performer-hit-name">{hit.name}</span>
                    {performerSub(hit) && <span className="muted">{performerSub(hit)}</span>}
                  </span>
                  <span className="btn btn-secondary btn-sm" aria-hidden="true">
                    查看主页
                  </span>
                </Link>
              ))}
            </div>
          )}

          {(browse || releases.length > 0) && (
            <FilterBar
              filter={filter}
              onChange={update}
              sortOptions={SORTS}
              sortValue={filter.sort ?? (browse ? 'newest' : 'relevance')}
              onSort={value => {
                if (value === 'relevance') update({sort: undefined, year: undefined, min_runtime: undefined, max_runtime: undefined});
                else update({sort: value});
              }}
            />
          )}

          {browse ? (
            <section className="shelf">
              {performers.length > 0 && <SectionHeader title="视频" />}
              <ReleaseResults
                filter={{...filter, q, sort: filter.sort ?? 'newest'}}
                onClear={clearFilters}
                clearLabel="按相关度查看结果"
                gridClassName="card-grid card-grid-4 search-grid"
                empty={
                  <EmptyState
                    icon="search"
                    title={`没有符合条件的「${q}」相关视频`}
                    body="试试放宽时长或年份。"
                  >
                    <div className="empty-actions">
                      <button type="button" className="btn btn-primary" onClick={clearFilters}>
                        清除筛选
                      </button>
                    </div>
                  </EmptyState>
                }
              />
            </section>
          ) : results.error ? (
            <ErrorBanner error={results.error} onRetry={results.reload} />
          ) : results.slow ? (
            <CardGridSkeleton count={8} />
          ) : results.data && results.data.length === 0 ? (
            <EmptyState
              icon="search"
              title={`没有找到「${q}」相关的视频`}
              body="试试换个关键词，或者用日文标题 / 演员名搜索。"
              actions={[{to: '/', label: '去首页逛逛'}, {to: '/discover?tab=actors', label: '浏览演员库', primary: false}]}
            />
          ) : (
            releases.length > 0 && (
              <section className="shelf">
                {performers.length > 0 && <SectionHeader title="视频" />}
                <div className="card-grid card-grid-4 search-grid">
                  {releases.map(hit => (
                    <ReleaseCard
                      key={hit.id}
                      slug={hit.slug}
                      title={hit.name}
                      badge={hit.runtime_minutes ? `${hit.runtime_minutes} 分` : undefined}
                      meta={[hit.maker_name, formatDate(hit.release_date)].filter(Boolean).join(' · ') || undefined}
                    />
                  ))}
                </div>
              </section>
            )
          )}
        </>
      )}
    </div>
  );
}

function performerSub(hit: SearchHit): string {
  const year = formatYear(hit.birth_date);
  return [hit.canonical_name_ja, year && `${year} 年生`].filter(Boolean).join(' · ');
}
