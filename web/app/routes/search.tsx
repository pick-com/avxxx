import {useEffect, useRef, useState} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {FollowButton} from '../components/SocialButtons';
import {Avatar, ReleaseCard, SectionHeader} from '../components/Cards';
import {FilterBar, ReleaseResults, type SortOption} from '../components/ReleaseFilters';
import {HotSearches} from '../components/HotSearches';
import {SearchBox} from '../components/SearchBox';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {onUnload, track} from '../lib/analytics';
import {type ReleaseFilter} from '../lib/api';
import {formatDate, formatYear} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {clearRecentSearches, readRecentSearches, rememberSearch} from '../lib/recentSearches';
import {RELEASE_SORTS, parseReleaseFilter, withFilter} from '../lib/releaseFilter';
import {searchCatalog, searchJump, type SearchItem, type SearchResponse} from '../lib/search';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: t('title.search')}];

const LIMIT = 60;
const RETRY_WINDOW_MS = 2 * 60_000;

// PRD 14 `search`: one event per search, sent when it ends — on a result
// click (with clicked_rank, 1-based in display order: performers, then
// videos), on the next query, on leaving the page. is_retry: the previous
// search in this tab ended without a click less than 2 minutes earlier.
// rescued: the server's modifier-stripped retry produced the results.
let lastSearch: {at: number; clicked: boolean} | null = null;
type PendingSearch = {query: string; count: number; isRetry: boolean; rescued: boolean};

function useSearchTracking(q: string, res: SearchResponse | undefined) {
  const pending = useRef<PendingSearch | null>(null);
  const finish = (rank?: number) => {
    const p = pending.current;
    if (!p) return;
    pending.current = null;
    lastSearch = {at: Date.now(), clicked: rank !== undefined};
    track('search', {
      query: p.query.slice(0, 200),
      result_count: p.count,
      is_retry: p.isRetry,
      clicked_rank: rank,
      rescued: p.rescued || undefined,
    });
  };
  const finishRef = useRef(finish);
  finishRef.current = finish;
  useEffect(() => {
    if (!q || !res) return;
    const isRetry = !!lastSearch && !lastSearch.clicked && Date.now() - lastSearch.at < RETRY_WINDOW_MS;
    pending.current = {query: q, count: res.items.length, isRetry, rescued: !!res.rescued};
    const off = onUnload(() => finishRef.current());
    return () => {
      off();
      finishRef.current();
    };
  }, [q, res]);
  return (rank: number) => finishRef.current(rank);
}
const SORTS: SortOption[] = [{value: 'relevance', get label() { return t('search.relevance'); }}, ...RELEASE_SORTS];

// Browse / MobileSearch / MobileSearchTyping.
//
// Default (相关度): GET /api/catalog/search — performers first, then releases.
// The envelope (search backend) drives query understanding: exact_match (a
// single product-code hit) and redirect (an AdminSearch rule) replace this
// page with the target; rescued (the modifier-stripped retry matched) shows
// 已为你搜索「…」; each item's `match` adds a subtle 别名匹配 / 英文标题 hint.
// Choosing a sort, 时长 or 年份 switches the video results to
// GET /api/releases?q=… (title / product-code match) with cursor paging.
// The empty state shows 最近搜索 and 大家都在搜 (GET /api/search/hot).
// TODO(tech plan 3.4): 类型 / 清晰度 / 字幕 filters, result counts.
export default function SearchPage() {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const q = (params.get('q') ?? '').trim();
  const filter = parseReleaseFilter(params);
  const browse = !!(filter.sort || filter.year || filter.min_runtime || filter.max_runtime || filter.tag || filter.maker || filter.performer);
  const navigate = useNavigate();
  const results = useAsync(signal => searchCatalog(q, LIMIT, signal), [q], q !== '');
  const [recent, setRecent] = useState<string[]>([]);
  const jump = !browse && results.data ? searchJump(results.data) : null;

  useEffect(() => {
    if (q) rememberSearch(q);
    setRecent(readRecentSearches());
  }, [q]);

  const hits = results.data?.items;
  const performers = (hits ?? []).filter(h => h.type === 'performer');
  const releases = (hits ?? []).filter(h => h.type === 'release');
  const clicked = useSearchTracking(q, results.data);
  const effective = results.data?.effective_query;
  const rescued = !!results.data?.rescued && !!effective;

  // exact_match counts as opening the first result; a redirect ends the search without a click.
  useEffect(() => {
    if (!jump) return;
    if (results.data?.exact_match && !results.data.redirect) clicked(1);
    if (jump.external) window.location.replace(jump.to);
    else navigate(jump.to, {replace: true});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jump?.to]);

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
                title={t('search.recent')}
                action={
                  <button
                    type="button"
                    className="text-btn"
                    onClick={() => {
                      clearRecentSearches();
                      setRecent([]);
                    }}
                  >
                    {t('common.clear')}
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
            <EmptyState icon="search" title={t('search.emptyTitle')} body={t('search.emptyBody')} actions={[{to: '/discover', label: t('search.browseDiscover'), primary: false}]} />
          )}
          <HotSearches variant="list" />
        </section>
      ) : (
        <>
          <div className="search-head">
            <h1>{t('search.heading', {q})}</h1>
            {!browse && hits && hits.length > 0 && (
              <span className="muted">{hits.length >= LIMIT ? t('search.firstN', {n: LIMIT}) : t('search.count', {n: hits.length})}</span>
            )}
          </div>
          {!browse && effective && effective !== results.data?.query && hits && hits.length > 0 && (
            <p className={rescued ? 'search-note search-note-rescued' : 'search-note'} role="status">
              {rescued ? t('search.rescued', {q, effective}) : t('search.synonym', {effective})}
            </p>
          )}

          {performers.length > 0 && (
            <div className="performer-hits">
              {performers.map((hit, i) => (
                <div key={hit.id} className="performer-hit">
                  <Link
                    to={`/performers/${encodeURIComponent(hit.slug)}`}
                    className="performer-hit-link"
                    state={{slot: `search:p${i + 1}`}}
                    onClick={() => {
                      clicked(i + 1);
                      track('actor_card_open', {performer_id: hit.id, source: 'search'});
                    }}
                  >
                    <Avatar seed={hit.slug} name={hit.name} size={64} />
                    <span className="performer-hit-text">
                      <span className="performer-hit-kind">{t('search.kindActor')}</span>
                      <span className="performer-hit-name">{hit.name}</span>
                      {performerSub(hit) && <span className="muted">{performerSub(hit)}</span>}
                      <MatchHint hit={hit} />
                    </span>
                  </Link>
                  <FollowButton slug={hit.slug} name={hit.name} performerId={hit.id} source="search" size="sm" />
                </div>
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
              {performers.length > 0 && <SectionHeader title={t('search.videos')} />}
              <ReleaseResults
                filter={{...filter, q, sort: filter.sort ?? 'newest'}}
                onClear={clearFilters}
                clearLabel={t('search.byRelevance')}
                gridClassName="card-grid card-grid-4 search-grid"
                empty={
                  <EmptyState
                    icon="search"
                    title={t('search.noFiltered', {q})}
                    body={t('search.noFilteredBody')}
                  >
                    <div className="empty-actions">
                      <button type="button" className="btn btn-primary" onClick={clearFilters}>
                        {t('common.clearFilters')}
                      </button>
                    </div>
                  </EmptyState>
                }
              />
            </section>
          ) : results.error ? (
            <ErrorBanner error={results.error} onRetry={results.reload} />
          ) : results.slow || jump ? (
            jump ? <p className="muted">{t('search.redirecting')}</p> : <CardGridSkeleton count={8} />
          ) : hits && hits.length === 0 ? (
            <EmptyState
              icon="search"
              title={t('search.none', {q})}
              body={t('search.noneBody')}
              actions={[{to: '/', label: t('common.goHome')}, {to: '/discover?tab=actors', label: t('common.browseActors'), primary: false}]}
            />
          ) : (
            releases.length > 0 && (
              <section className="shelf">
                {performers.length > 0 && <SectionHeader title={t('search.videos')} />}
                <div className="card-grid card-grid-4 search-grid">
                  {releases.map((hit, i) => (
                    <ReleaseCard
                      key={hit.id}
                      slot={`search:r${i + 1}`}
                      onOpen={() => clicked(performers.length + i + 1)}
                      slug={hit.slug}
                      title={hit.name}
                      badge={hit.runtime_minutes ? t('card.minutes', {n: hit.runtime_minutes}) : undefined}
                      meta={[hit.maker_name, formatDate(hit.release_date)].filter(Boolean).join(' · ') || undefined}
                      sub={matchHint(hit) ? <MatchHint hit={hit} /> : undefined}
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

/** Subtle note for non-obvious matches; plain name / title matches show nothing. */
function matchHint(hit: SearchItem): string {
  switch (hit.match) {
    case 'alias':
      return t('search.match.alias');
    case 'translation':
      return hit.type === 'release' ? t('search.match.translationTitle') : t('search.match.translation');
    case 'fuzzy':
      return t('search.match.fuzzy');
    case 'code':
      return t('search.match.code');
  }
  return '';
}

function MatchHint({hit}: {hit: SearchItem}) {
  const text = matchHint(hit);
  return text ? <span className="match-hint">{text}</span> : null;
}

function performerSub(hit: SearchItem): string {
  const year = formatYear(hit.birth_date);
  return [hit.canonical_name_ja, year && t('search.born', {year})].filter(Boolean).join(' · ');
}
