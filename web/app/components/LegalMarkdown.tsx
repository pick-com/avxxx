import type {ReactNode} from 'react';
import {parseInline, type Inline, type LegalBlock} from '../lib/legalMarkdown';

// Renders lib/legalMarkdown.ts blocks (the legal_documents.body Markdown
// subset) as React elements, never innerHTML, so document text cannot inject
// markup; link targets are already limited to http(s), mailto and relative.
// Used by the public legal pages and the back-office preview.

export {legalHeadings, parseLegalMarkdown, type LegalHeading} from '../lib/legalMarkdown';

function inline(nodes: Inline[]): ReactNode[] {
  return nodes.map((n, i) => {
    switch (n.kind) {
      case 'text':
        return n.text;
      case 'strong':
        return <strong key={i}>{inline(n.children)}</strong>;
      case 'em':
        return <em key={i}>{inline(n.children)}</em>;
      default:
        return (
          <a key={i} href={n.href} className="meta-link" {...(n.external ? {target: '_blank', rel: 'noopener noreferrer'} : {})}>
            {inline(n.children)}
          </a>
        );
    }
  });
}

const text = (s: string) => inline(parseInline(s));

/** The document body; h2 sections are numbered like the old template pages. */
export function LegalMarkdown({blocks}: {blocks: LegalBlock[]}) {
  let n = 0;
  return (
    <>
      {blocks.map((b, i) => {
        switch (b.kind) {
          case 'h2':
            n++;
            return (
              <h2 key={i} id={b.id} className="legal-h2">
                {n}. {text(b.text)}
              </h2>
            );
          case 'h3':
            return (
              <h3 key={i} id={b.id} className="legal-h3">
                {text(b.text)}
              </h3>
            );
          case 'ul':
          case 'ol': {
            const List = b.kind;
            return (
              <List key={i} className="legal-list">
                {b.items.map((it, j) => (
                  <li key={j}>{text(it)}</li>
                ))}
              </List>
            );
          }
          default:
            return (
              <p key={i} className="legal-p">
                {text(b.text)}
              </p>
            );
        }
      })}
    </>
  );
}
