import {useEffect, useMemo} from 'react';
import {data, useLoaderData, useParams, useSearchParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {Link} from '../lib/nav';
import {LegalMarkdown, legalHeadings, parseLegalMarkdown} from '../components/LegalMarkdown';
import {PerformerRequestForm} from '../components/PerformerRequestForm';
import {BlockSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, type LangCode} from '../lib/api';
import {isGone, serverApi} from '../lib/api.server';
import {
  LEGAL_KINDS,
  getLegalDocument,
  getLegalVersions,
  isLegalKind,
  legalDocumentPath,
  legalVersionsPath,
  type LegalDocument,
  type LegalKind,
  type LegalVersionInfo,
} from '../lib/compliance';
import {formatDate} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {DEFAULT_LANG, LANG_LABELS, routeLang} from '../lib/langs';
import type {MessageKey} from '../lib/messages.zh-CN';
import {seoMeta, type SeoHandle} from '../lib/seo';
import {useAsync} from '../lib/useAsync';
import '../styles/compliance.css';

export const handle: SeoHandle = {seo: true};

// LegalPage (PRD 15): the texts are versioned rows of legal_documents
// (migration 041), served by GET /api/legal/{kind}?lang=&version= with a
// language fallback. The loader renders the document on the server (SEO,
// no-JS); client navigations fetch it and the loader's data seeds useAsync.
// ?v=N shows an older (or announced) version; the selector lists them all.

const TITLE_KEYS: Record<LegalKind, MessageKey> = {
  privacy: 'footer.privacy',
  terms: 'footer.terms',
  cookies: 'footer.cookies',
  copyright: 'footer.copyright',
  community: 'footer.community',
  ads: 'footer.ads',
  'performer-info': 'footer.performerInfo',
};

/** Marker the template texts still carry until counsel signs them off. */
const DRAFT_MARKERS = ['[待律师审阅]', '[Pending legal review]'];

const versionParam = (raw: string | null): number | undefined => {
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : undefined;
};

type LegalLoad = {
  kind: string;
  lang: LangCode;
  version?: number;
  doc?: LegalDocument;
  versions?: LegalVersionInfo[];
  missing?: boolean;
  unavailable?: boolean;
};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  if (!lang) return null;
  const kind = params.doc ?? '';
  const version = versionParam(new URL(request.url).searchParams.get('v'));
  if (!isLegalKind(kind)) return data({kind, lang, version, missing: true} satisfies LegalLoad, {status: 404});
  try {
    const [doc, versions] = await Promise.all([
      serverApi<LegalDocument>(legalDocumentPath(kind, lang, version), {signal: request.signal}),
      serverApi<{items: LegalVersionInfo[] | null}>(legalVersionsPath(kind, lang), {signal: request.signal}).then(
        d => d?.items ?? [],
        () => [],
      ),
    ]);
    return {kind, lang, version, doc, versions} satisfies LegalLoad;
  } catch (e) {
    if (isGone(e)) return data({kind, lang, version, missing: true} satisfies LegalLoad, {status: 404});
    console.error('legal loader', kind, e);
    return data({kind, lang, version, unavailable: true} satisfies LegalLoad, {status: 503});
  }
}

export const clientLoader = () => null;

export const meta: MetaFunction<typeof loader> = ({data: d, params, matches}) => {
  const load = d as LegalLoad | null | undefined;
  const path = `/legal/${encodeURIComponent(params.doc ?? '')}`;
  const doc = load?.doc;
  if (!doc) return seoMeta({matches, path, title: t('title.legal'), noindex: !!load?.missing});
  return seoMeta({
    matches,
    path,
    title: t('title.named', {name: doc.title}),
    description: doc.summary.join(' ') || t('seo.legalDescription', {title: doc.title}),
    ogType: 'article',
    // Older versions stay reachable but are not what search should show.
    noindex: !doc.is_current,
  });
};

export default function LegalPage() {
  const t = useT();
  const params = useParams();
  const kind = params.doc ?? '';
  const lang = routeLang(params) ?? DEFAULT_LANG;
  const [search, setSearch] = useSearchParams();
  const version = versionParam(search.get('v'));
  const loaded = useLoaderData<typeof loader>() as LegalLoad | null;
  const own = loaded && loaded.kind === kind && loaded.lang === lang && loaded.version === version ? loaded : null;
  const known = isLegalKind(kind);
  const seed = own?.doc ? ([own.doc, own.versions ?? []] as [LegalDocument, LegalVersionInfo[]]) : undefined;
  const load = useAsync(
    signal => Promise.all([getLegalDocument(kind, lang, version, signal), getLegalVersions(kind, lang, signal).catch(() => [] as LegalVersionInfo[])]),
    [kind, lang, version],
    known && !own?.missing,
    seed,
  );
  const doc = load.data?.[0];
  const versions = load.data?.[1] ?? [];
  const blocks = useMemo(() => (doc ? parseLegalMarkdown(doc.body) : []), [doc]);
  const headings = useMemo(() => legalHeadings(blocks), [blocks]);

  useEffect(() => {
    if (doc) document.title = t('title.named', {name: doc.title});
  }, [doc]);

  // Client-side navigation to /legal/x#section: scroll once the section exists.
  useEffect(() => {
    const id = decodeURIComponent(window.location.hash.slice(1));
    if (id && doc) document.getElementById(id)?.scrollIntoView();
  }, [doc]);

  const missing = !known || own?.missing || (load.error instanceof ApiError && load.error.status === 404);
  const nav = (
    <nav className="legal-docs" aria-label={t('legal.docs')}>
      {LEGAL_KINDS.map(k => (
        <Link key={k} to={`/legal/${k}`} className="legal-doc-link" aria-current={k === kind ? 'page' : undefined}>
          {t(TITLE_KEYS[k])}
        </Link>
      ))}
    </nav>
  );

  if (missing) {
    return (
      <div className="page">
        <EmptyState title={t('legal.notFound')} body={t('legal.notFoundBody')} actions={[{to: '/legal/privacy', label: t('legal.viewPrivacy')}]} />
      </div>
    );
  }

  const pickVersion = (v: string) => {
    const next = new URLSearchParams(search);
    const n = Number(v);
    if (!n || versions.find(x => x.version === n)?.is_current) next.delete('v');
    else next.set('v', v);
    setSearch(next, {preventScrollReset: true});
  };
  const draft = doc && DRAFT_MARKERS.some(m => doc.body.includes(m) || doc.summary.some(s => s.includes(m)));
  const upcoming = doc && !doc.is_current && doc.version > doc.current_version;

  return (
    <div className="page legal-page">
      {nav}
      <article className="legal-body">
        {!doc ? (
          load.error || own?.unavailable ? (
            <ErrorBanner error={t('legal.unavailable')} onRetry={load.reload} />
          ) : (
            <BlockSkeleton />
          )
        ) : (
          <>
            {draft && (
              <p className="draft-flag" role="note">
                {t('legal.draft')}
              </p>
            )}
            <h1 className="page-title">{doc.title}</h1>
            <div className="legal-meta">
              <span className="muted">{t('legal.dates', {version: doc.version, date: formatDate(doc.effective_at)})}</span>
              {versions.length > 1 && (
                <label className="legal-version">
                  <span className="muted">{t('legal.version')}</span>
                  <select className="input" value={doc.version} onChange={e => pickVersion(e.target.value)}>
                    {versions.map(v => (
                      <option key={v.version} value={v.version}>
                        {t('legal.versionOption', {version: v.version, date: formatDate(v.effective_at)})}
                        {v.is_current ? ` ${t('legal.versionCurrent')}` : ''}
                      </option>
                    ))}
                  </select>
                </label>
              )}
            </div>
            {!doc.is_current && (
              <p className="legal-notice" role="status">
                {upcoming ? t('legal.upcomingVersion', {date: formatDate(doc.effective_at)}) : t('legal.oldVersion', {version: doc.version})}{' '}
                <Link to={`/legal/${kind}`} className="meta-link">
                  {t('legal.viewCurrent')}
                </Link>
              </p>
            )}
            {doc.fallback && (
              <p className="legal-notice" role="note">
                {t('legal.fallback', {lang: LANG_LABELS[doc.requested_lang] ?? doc.requested_lang, served: LANG_LABELS[doc.lang] ?? doc.lang})}
              </p>
            )}
            {doc.summary.length > 0 && (
              <section className="legal-summary" aria-labelledby="legal-tldr">
                <h2 id="legal-tldr">{t('legal.tldr')}</h2>
                <ul>
                  {doc.summary.map(line => (
                    <li key={line}>{line}</li>
                  ))}
                </ul>
              </section>
            )}
            {headings.length > 1 && (
              <nav className="legal-toc" aria-label={t('legal.toc')}>
                <span className="peek-label">{t('legal.toc')}</span>
                {headings.map(h => (
                  <a key={h.id} href={`#${h.id}`} className="meta-link">
                    {h.title}
                  </a>
                ))}
              </nav>
            )}
            <div className="legal-section legal-md">
              <LegalMarkdown blocks={blocks} />
            </div>
          </>
        )}
        {kind === 'copyright' && (
          <aside className="legal-other" aria-labelledby="legal-copyright-title">
            <h2 id="legal-copyright-title">{t('legal.copyrightCta')}</h2>
            <span className="muted">{t('legal.copyrightCtaBody')}</span>
            <Link to="/copyright">{t('legal.copyrightCtaLink')}</Link>
          </aside>
        )}
        {kind === 'copyright' && (
          <aside className="legal-other" aria-labelledby="legal-other-title">
            <h2 id="legal-other-title">{t('performerReq.otherCases')}</h2>
            <span className="muted">{t('performerReq.copyrightLink')}</span>
            <Link to="/legal/performer-info#request-form">{t('performerReq.cta')}</Link>
          </aside>
        )}
        {kind === 'performer-info' && <PerformerRequestForm />}
      </article>
    </div>
  );
}
