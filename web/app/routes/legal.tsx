import {useEffect} from 'react';
import {Link, useParams} from 'react-router';
import {EmptyState} from '../components/States';
import {t, useT} from '../lib/i18n';
import {LEGAL_DOCS, legalDoc} from '../lib/legal';

export const meta = () => [{title: t('title.legal')}];

// LegalPage template: document list, 简要版, sections with a table of contents.
// All text is placeholder until counsel signs off (PRD 15).
export default function LegalPage() {
  const t = useT();
  const {doc = ''} = useParams();
  const current = legalDoc(doc);

  useEffect(() => {
    if (current) document.title = t('title.named', {name: current.title});
  }, [current]);

  if (!current) {
    return (
      <div className="page">
        <EmptyState title={t('legal.notFound')} body={t('legal.notFoundBody')} actions={[{to: '/legal/privacy', label: t('legal.viewPrivacy')}]} />
      </div>
    );
  }

  return (
    <div className="page legal-page">
      <nav className="legal-docs" aria-label={t('legal.docs')}>
        {LEGAL_DOCS.map(d => (
          <Link key={d.slug} to={`/legal/${d.slug}`} className="legal-doc-link" aria-current={d.slug === current.slug ? 'page' : undefined}>
            {d.title}
          </Link>
        ))}
      </nav>
      <article className="legal-body">
        <p className="draft-flag" role="note">
          {t('legal.draft')}
        </p>
        <h1 className="page-title">{current.title}</h1>
        <p className="muted">{t('legal.dates')}</p>
        <section className="legal-summary" aria-labelledby="legal-tldr">
          <h2 id="legal-tldr">{t('legal.tldr')}</h2>
          <ul>
            {current.summary.map(line => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </section>
        <nav className="legal-toc" aria-label={t('legal.toc')}>
          <span className="peek-label">{t('legal.toc')}</span>
          {current.sections.map(s => (
            <a key={s.id} href={`#${s.id}`} className="meta-link">
              {s.title}
            </a>
          ))}
        </nav>
        {current.sections.map((s, i) => (
          <section key={s.id} id={s.id} className="legal-section">
            <h2>
              {i + 1}. {s.title}
            </h2>
            <p>{s.body}</p>
          </section>
        ))}
      </article>
    </div>
  );
}
