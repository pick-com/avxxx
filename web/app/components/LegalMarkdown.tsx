import type {ReactNode} from 'react';

// The small Markdown subset legal_documents.body uses (migration 041):
// `## Heading {#anchor}` / `### Sub`, paragraphs separated by blank lines,
// `- ` list items, **bold** and [text](https://… or /path) links. Rendered
// to React elements (never innerHTML), so document text cannot inject markup.

export type LegalHeading = {id: string; title: string};
type Block =
  | {kind: 'h2' | 'h3'; id: string; text: string}
  | {kind: 'p'; text: string}
  | {kind: 'ul'; items: string[]};

const HEADING = /^(#{2,3})\s+(.*?)(?:\s+\{#([A-Za-z0-9_-]+)\})?\s*$/;

const slug = (s: string, i: number) => {
  const v = s
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '-')
    .replace(/^-+|-+$/g, '');
  return v || `section-${i + 1}`;
};

export function parseLegalMarkdown(md: string): Block[] {
  const blocks: Block[] = [];
  let para: string[] = [];
  let list: string[] | null = null;
  const flush = () => {
    if (para.length) blocks.push({kind: 'p', text: para.join(' ')});
    if (list) blocks.push({kind: 'ul', items: list});
    para = [];
    list = null;
  };
  for (const raw of md.replace(/\r\n/g, '\n').split('\n')) {
    const line = raw.trimEnd();
    const h = HEADING.exec(line);
    if (h) {
      flush();
      blocks.push({kind: h[1].length === 2 ? 'h2' : 'h3', text: h[2], id: h[3] || slug(h[2], blocks.length)});
    } else if (/^\s*[-*]\s+/.test(line)) {
      if (para.length) flush();
      (list ??= []).push(line.replace(/^\s*[-*]\s+/, ''));
    } else if (!line.trim()) {
      flush();
    } else {
      if (list) flush();
      para.push(line.trim());
    }
  }
  flush();
  return blocks;
}

export const legalHeadings = (blocks: Block[]): LegalHeading[] =>
  blocks.flatMap(b => (b.kind === 'h2' ? [{id: b.id, title: b.text}] : []));

const INLINE = /\*\*(.+?)\*\*|\[([^\]]+)\]\(((?:https?:\/\/|\/)[^\s)]+)\)/g;

function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1] !== undefined) out.push(<strong key={m.index}>{m[1]}</strong>);
    else
      out.push(
        <a key={m.index} href={m[3]} className="meta-link" {...(m[3].startsWith('/') ? {} : {target: '_blank', rel: 'noopener noreferrer'})}>
          {m[2]}
        </a>,
      );
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** The document body; h2 sections are numbered like the old template pages. */
export function LegalMarkdown({blocks}: {blocks: Block[]}) {
  let n = 0;
  return (
    <>
      {blocks.map((b, i) => {
        switch (b.kind) {
          case 'h2':
            n++;
            return (
              <h2 key={i} id={b.id} className="legal-h2">
                {n}. {inline(b.text)}
              </h2>
            );
          case 'h3':
            return (
              <h3 key={i} id={b.id} className="legal-h3">
                {inline(b.text)}
              </h3>
            );
          case 'ul':
            return (
              <ul key={i} className="legal-list">
                {b.items.map((it, j) => (
                  <li key={j}>{inline(it)}</li>
                ))}
              </ul>
            );
          default:
            return (
              <p key={i} className="legal-p">
                {inline(b.text)}
              </p>
            );
        }
      })}
    </>
  );
}
