import {useEffect} from 'react';
import {Link, useParams} from 'react-router';
import {EmptyState} from '../components/States';
import {LEGAL_DOCS, legalDoc} from '../lib/legal';

export const meta = () => [{title: 'REEL · 法律与政策'}];

// LegalPage template: document list, 简要版, sections with a table of contents.
// All text is placeholder until counsel signs off (PRD 15).
export default function LegalPage() {
  const {doc = ''} = useParams();
  const current = legalDoc(doc);

  useEffect(() => {
    if (current) document.title = `${current.title} · REEL`;
  }, [current]);

  if (!current) {
    return (
      <div className="page">
        <EmptyState title="没有找到这份文件" body="链接可能有误。" actions={[{to: '/legal/privacy', label: '查看隐私政策'}]} />
      </div>
    );
  }

  return (
    <div className="page legal-page">
      <nav className="legal-docs" aria-label="法律文件">
        {LEGAL_DOCS.map(d => (
          <Link key={d.slug} to={`/legal/${d.slug}`} className="legal-doc-link" aria-current={d.slug === current.slug ? 'page' : undefined}>
            {d.title}
          </Link>
        ))}
      </nav>
      <article className="legal-body">
        <p className="draft-flag" role="note">
          [待律师审阅] 本页为模板，正文尚未定稿，不构成正式条款。
        </p>
        <h1 className="page-title">{current.title}</h1>
        <p className="muted">生效日期 [待定] · 上次更新 [待定]</p>
        <section className="legal-summary" aria-labelledby="legal-tldr">
          <h2 id="legal-tldr">简要版</h2>
          <ul>
            {current.summary.map(line => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </section>
        <nav className="legal-toc" aria-label="本页目录">
          <span className="peek-label">本页目录</span>
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
