import {useT} from '../lib/i18n';
import type {Attribution} from '../lib/evidence';
import '../styles/series.css';

/**
 * Credits that sources require wherever their data is displayed
 * (sources.attribution_html; DMM: `Powered by <a href="https://affiliate.dmm.com/api/">FANZA Webサービス</a>`).
 * Each html is rendered exactly as stored, with no attributes or markup
 * added inside it; identical credits from several sources show once.
 * Placed at the end of release, performer and series pages (their footer).
 */
export function SourceCredits({items}: {items: Attribution[]}) {
  const t = useT();
  const unique = Array.from(new Set(items.map(a => a.html).filter(html => html.trim() !== '')));
  if (unique.length === 0) return null;
  return (
    <footer className="source-credits" aria-label={t('credits.label')}>
      {unique.map(html => (
        <p key={html} className="source-credit" dangerouslySetInnerHTML={{__html: html}} />
      ))}
    </footer>
  );
}
