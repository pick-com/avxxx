import {useState} from 'react';
import {fieldLabel, getEvidence, tierLabel, type EvidenceKind, type FieldEvidence} from '../lib/evidence';
import {formatDate, formatRuntime} from '../lib/format';
import {useT} from '../lib/i18n';
import {useAsync} from '../lib/useAsync';
import '../styles/series.css';

const DATE_FIELDS = new Set(['release_date', 'birth_date', 'debut_date']);
const CM_FIELDS = new Set(['height_cm', 'bust_cm', 'waist_cm', 'hip_cm']);
const LONG_FIELDS = new Set(['synopsis', 'biography']);

const isEmpty = (v: unknown) => v === null || v === undefined || (typeof v === 'string' && v.trim() === '');

function shorten(text: string, max: number) {
  const s = text.replace(/\s+/g, ' ').trim();
  return s.length > max ? `${s.slice(0, max - 1).trimEnd()}…` : s;
}

function showValue(field: string, value: unknown): string {
  if (isEmpty(value)) return '—';
  if (DATE_FIELDS.has(field) && typeof value === 'string') return formatDate(value) || value;
  if (field === 'runtime_minutes' && Number(value) > 0) return formatRuntime(Number(value));
  if (CM_FIELDS.has(field) && Number.isFinite(Number(value))) return `${Number(value)} cm`;
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  return shorten(text, LONG_FIELDS.has(field) ? 60 : 120);
}

/**
 * 信息来源: per field, the site's value, which source provided it (with its
 * tier), when it was last updated, and an 人工校正 badge for admin overrides.
 * Closed by default and loaded only when opened, so it never delays the
 * server-rendered page. Hidden fields never reach the client (the API omits them).
 */
export function SourceEvidence({kind, slug}: {kind: EvidenceKind; slug: string}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const ev = useAsync(signal => getEvidence(kind, slug, signal), [kind, slug], open);
  const fields = (ev.data?.fields ?? []).filter(f => !isEmpty(f.value) || f.overridden || (f.sources ?? []).length > 0);
  const sources = ev.data?.sources ?? [];

  return (
    <details className="evidence" onToggle={e => setOpen(e.currentTarget.open)}>
      <summary className="evidence-summary">
        <span className="evidence-title">{t('evidence.title')}</span>
        <span className="evidence-hint">{t('evidence.hint')}</span>
      </summary>
      {open && (
        <div className="evidence-body">
          {ev.error ? (
            <p className="evidence-status" role="alert">
              {t('evidence.failed')}{' '}
              <button type="button" className="text-btn" onClick={ev.reload}>
                {t('evidence.retry')}
              </button>
            </p>
          ) : !ev.data ? (
            <p className="evidence-status" aria-busy="true">
              {t('evidence.loading')}
            </p>
          ) : fields.length === 0 && sources.length === 0 ? (
            <p className="evidence-status">{t('evidence.empty')}</p>
          ) : (
            <>
              {ev.data.updated_at && <p className="evidence-status">{t('evidence.lastUpdated', {date: formatDate(ev.data.updated_at)})}</p>}
              <dl className="evidence-list">
                {fields.map(f => (
                  <EvidenceRow key={f.field} f={f} />
                ))}
              </dl>
              {sources.length > 0 && (
                <div className="evidence-sources">
                  <h3 className="evidence-subtitle">{t('evidence.sources')}</h3>
                  <ul>
                    {sources.map(s => (
                      <li key={`${s.name}|${s.url ?? ''}`}>
                        <span className="evidence-source-name">{s.name}</span>
                        <span className="evidence-tier">{t(tierLabel(s.tier))}</span>
                        <span className="evidence-meta">{t('evidence.lastSeen', {date: formatDate(s.last_seen_at)})}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </details>
  );
}

function EvidenceRow({f}: {f: FieldEvidence}) {
  const t = useT();
  const label = fieldLabel(f.field);
  const others = (f.sources ?? []).filter(s => !s.matches && !isEmpty(s.value));
  const provider = f.overridden ? null : f.provided_by;
  return (
    <div className="evidence-row">
      <dt>
        {label ? t(label) : f.field}
        {f.overridden && <span className="evidence-badge">{t('evidence.manual')}</span>}
      </dt>
      <dd>
        <span className="evidence-value">{showValue(f.field, f.value)}</span>
        <span className="evidence-meta">
          {provider ? (
            <>
              <span className="evidence-source-name">{provider.source}</span>
              <span className="evidence-tier">{t(tierLabel(provider.tier))}</span>
            </>
          ) : (
            // Only when sources disagree; a field no source reports just shows its value.
            !f.overridden && (f.sources ?? []).length > 0 && <span>{t('evidence.noProvider')}</span>
          )}
          {f.updated_at && <span>{t('evidence.updatedAt', {date: formatDate(f.updated_at)})}</span>}
        </span>
        {others.length > 0 && (
          <span className="evidence-meta evidence-others">
            {t('evidence.otherValues')}:{' '}
            {others.map((s, i) => (
              <span key={`${s.source}|${i}`}>
                {i > 0 && ' · '}
                {showValue(f.field, s.value)} ({s.source})
              </span>
            ))}
          </span>
        )}
      </dd>
    </div>
  );
}
