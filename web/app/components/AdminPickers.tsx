import {useEffect, useState} from 'react';
import {isAbort, listPerformerPage, listReleasePage} from '../lib/api';
import {getAdminReleases} from '../lib/adminApi';

export type Picked = {id: number; slug: string; name: string; sub?: string};

/** Quick picks (e.g. near matches) plus a debounced lookup by name. */
function Picker({
  label,
  placeholder,
  quick,
  value,
  onPick,
  lookup,
}: {
  label: string;
  placeholder: string;
  quick: Picked[];
  value: Picked | null;
  onPick: (p: Picked | null) => void;
  lookup: (q: string, signal: AbortSignal) => Promise<Picked[]>;
}) {
  const [q, setQ] = useState('');
  const [found, setFound] = useState<Picked[]>([]);
  const [error, setError] = useState('');

  useEffect(() => {
    const text = q.trim();
    if (!text) {
      setFound([]);
      return;
    }
    const ctrl = new AbortController();
    const timer = window.setTimeout(() => {
      lookup(text, ctrl.signal).then(
        v => !ctrl.signal.aborted && (setFound(v), setError('')),
        e => !isAbort(e) && !ctrl.signal.aborted && setError('查找失败'),
      );
    }, 250);
    return () => {
      ctrl.abort();
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);

  if (value) {
    return (
      <div className="adm-field">
        <span>{label}</span>
        <div className="adm-toolbar">
          <strong>{value.name}</strong>
          <span className="muted mono">#{value.id}</span>
          <button type="button" className="adm-link" onClick={() => onPick(null)}>
            重新选择
          </button>
        </div>
      </div>
    );
  }
  const list = [...quick, ...found.filter(f => !quick.some(x => x.id === f.id))];
  return (
    <div className="adm-field adm-picker">
      <span>{label}</span>
      <input value={q} onChange={e => setQ(e.target.value)} placeholder={placeholder} aria-label={label} />
      {error && <span className="adm-field-hint warn">{error}</span>}
      {list.length > 0 && (
        <div className="adm-chips">
          {list.map(p => (
            <button key={p.id} type="button" className="adm-chip" onClick={() => onPick(p)} title={p.slug}>
              {p.name}
              {p.sub && <span className="muted"> · {p.sub}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function PerformerPicker(props: {quick?: Picked[]; value: Picked | null; onPick: (p: Picked | null) => void; label?: string}) {
  return (
    <Picker
      label={props.label ?? '演员'}
      placeholder="按演员名查找"
      quick={props.quick ?? []}
      value={props.value}
      onPick={props.onPick}
      lookup={(q, signal) =>
        listPerformerPage('name', 8, null, q, signal).then(p =>
          p.items.map(x => ({id: x.id, slug: x.slug, name: x.canonical_name, sub: x.canonical_name_ja ?? undefined})),
        )
      }
    />
  );
}

export function ReleasePicker(props: {quick?: Picked[]; value: Picked | null; onPick: (p: Picked | null) => void; label?: string}) {
  return (
    <Picker
      label={props.label ?? '视频'}
      placeholder="按标题或番号查找"
      quick={props.quick ?? []}
      value={props.value}
      onPick={props.onPick}
      lookup={async (q, signal) => {
        try {
          const page = await getAdminReleases({q, limit: 8});
          return page.items.map(x => ({id: x.id, slug: x.slug, name: x.title, sub: (x.identifiers ?? [])[0]}));
        } catch (e) {
          if (isAbort(e)) throw e;
          const page = await listReleasePage({q}, 8, null, signal);
          return page.items.map(x => ({id: x.id, slug: x.slug, name: x.canonical_title}));
        }
      }}
    />
  );
}
