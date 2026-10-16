import {useEffect, useState, type DragEvent, type FormEvent, type KeyboardEvent} from 'react';
import {AdminNotice, BandPill, Pill, fmtTime, statusTone} from './AdminUI';
import {Icon} from './Icon';
import {
  KIND_LABELS,
  SOURCE_KINDS,
  VERSION_KINDS,
  VERSION_LABELS,
  createReleaseSource,
  promoteMediaSource,
  reorderReleaseSources,
  requiresLicense,
  setReleaseSourceEnabled,
  updateReleaseSource,
  type AdminMediaSource,
  type AdminPlaybackSource,
  type SourceInput,
} from '../lib/adminApi';
import {useAdminAction} from '../lib/adminContext';

// 片源 tab of the video editor (PR #61 admin/releases/{id}/sources*): list in
// priority order with version labels and health, drag or keyboard reorder
// (Alt+↑/↓ on a focused row), create / edit with the license_ref rule
// (store.RequiresLicense), disable / enable with a reason (required to
// re-enable a source the health job turned off), and promotion of crawled
// media sources.

const splitList = (s: string) =>
  s
    .split(/[\s,，]+/)
    .map(x => x.trim())
    .filter(Boolean);

type FormState = {
  kind: string;
  provider: string;
  label: string;
  url: string;
  storage_prefix: string;
  max_height: string;
  has_hdr: boolean;
  duration_seconds: string;
  audio_langs: string;
  subtitle_langs: string;
  version_kind: string;
  priority: string;
  region_allow: string;
  region_deny: string;
  license_ref: string;
};

const blank: FormState = {
  kind: 'official_embed',
  provider: '',
  label: '',
  url: '',
  storage_prefix: '',
  max_height: '',
  has_hdr: false,
  duration_seconds: '',
  audio_langs: '',
  subtitle_langs: '',
  version_kind: 'main',
  priority: '',
  region_allow: '',
  region_deny: '',
  license_ref: '',
};

function fromSource(s: AdminPlaybackSource): FormState {
  return {
    kind: s.kind,
    provider: s.provider,
    label: s.label,
    url: s.url ?? '',
    storage_prefix: s.storage_prefix ?? '',
    max_height: s.max_height?.toString() ?? '',
    has_hdr: s.has_hdr,
    duration_seconds: s.duration_seconds?.toString() ?? '',
    audio_langs: s.audio_langs.join(', '),
    subtitle_langs: s.subtitle_langs.join(', '),
    version_kind: s.version_kind,
    priority: String(s.priority),
    region_allow: (s.region_allow ?? []).join(', '),
    region_deny: (s.region_deny ?? []).join(', '),
    license_ref: s.license_ref ?? '',
  };
}

const optInt = (s: string) => (s.trim() === '' ? null : Number(s));

function toInput(f: FormState): SourceInput {
  return {
    kind: f.kind,
    provider: f.provider.trim(),
    label: f.label.trim(),
    url: f.url.trim() || null,
    storage_prefix: f.storage_prefix.trim() || null,
    max_height: optInt(f.max_height),
    has_hdr: f.has_hdr,
    duration_seconds: optInt(f.duration_seconds),
    audio_langs: splitList(f.audio_langs),
    subtitle_langs: splitList(f.subtitle_langs),
    version_kind: f.version_kind,
    priority: optInt(f.priority),
    region_allow: splitList(f.region_allow).map(r => r.toUpperCase()),
    region_deny: splitList(f.region_deny).map(r => r.toUpperCase()),
    license_ref: f.license_ref.trim() || null,
  };
}

function SourceForm({initial, title, busy, onSubmit, onCancel}: {
  initial: FormState;
  title: string;
  busy: boolean;
  onSubmit: (input: SourceInput) => void;
  onCancel: () => void;
}) {
  const [f, setF] = useState(initial);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF(prev => ({...prev, [k]: v}));
  const needsLicense = requiresLicense(f.kind);
  const hls = f.kind === 'hls_owned';
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit(toInput(f));
  };
  return (
    <form className="adm-panel" onSubmit={submit}>
      <h3>{title}</h3>
      <div className="adm-form-grid">
        <label className="adm-field">
          类型
          <select value={f.kind} onChange={e => set('kind', e.target.value)}>
            {SOURCE_KINDS.map(k => (
              <option key={k} value={k}>
                {KIND_LABELS[k]}（{k}）
              </option>
            ))}
          </select>
        </label>
        <label className="adm-field">
          提供方
          <input required maxLength={100} value={f.provider} onChange={e => set('provider', e.target.value)} placeholder="例如 FANZA" />
        </label>
        <label className="adm-field">
          名称
          <input required maxLength={100} value={f.label} onChange={e => set('label', e.target.value)} placeholder="例如 正片 · 1080p" />
        </label>
        <label className="adm-field">
          版本
          <select value={f.version_kind} onChange={e => set('version_kind', e.target.value)}>
            {VERSION_KINDS.map(k => (
              <option key={k} value={k}>
                {VERSION_LABELS[k]}
              </option>
            ))}
          </select>
        </label>
        {hls ? (
          <label className="adm-field span-all">
            存储前缀（storage_prefix，必填）
            <input required value={f.storage_prefix} onChange={e => set('storage_prefix', e.target.value)} placeholder="hls/abc-123/" />
          </label>
        ) : (
          <label className="adm-field span-all">
            URL（http/https，必填）
            <input required type="url" value={f.url} onChange={e => set('url', e.target.value)} placeholder="https://…" />
          </label>
        )}
        <label className="adm-field span-all">
          <span className="label-row">
            版权凭证 license_ref {needsLicense ? <Pill tone="amber">必填</Pill> : <Pill>可选</Pill>}
          </span>
          <input
            required={needsLicense}
            maxLength={200}
            value={f.license_ref}
            onChange={e => set('license_ref', e.target.value)}
            placeholder="合同或授权编号"
          />
          <span className="adm-field-hint">除官方预告与外部链接外，所有片源都必须有版权凭证（PRD 15）。</span>
        </label>
        <label className="adm-field">
          最高清晰度（像素高）
          <input type="number" min={1} max={4320} value={f.max_height} onChange={e => set('max_height', e.target.value)} placeholder="1080" />
        </label>
        <label className="adm-field">
          时长（秒）
          <input type="number" min={1} max={86400} value={f.duration_seconds} onChange={e => set('duration_seconds', e.target.value)} />
        </label>
        <label className="adm-field">
          优先级（越小越靠前，默认 100）
          <input type="number" min={0} max={100000} value={f.priority} onChange={e => set('priority', e.target.value)} />
        </label>
        <label className="adm-check" style={{alignSelf: 'end', minHeight: 36}}>
          <input type="checkbox" checked={f.has_hdr} onChange={e => set('has_hdr', e.target.checked)} /> HDR
        </label>
        <label className="adm-field">
          音轨语言
          <input value={f.audio_langs} onChange={e => set('audio_langs', e.target.value)} placeholder="ja, en" />
        </label>
        <label className="adm-field">
          字幕语言
          <input value={f.subtitle_langs} onChange={e => set('subtitle_langs', e.target.value)} placeholder="zh-CN, en" />
        </label>
        <label className="adm-field">
          仅限地区
          <input value={f.region_allow} onChange={e => set('region_allow', e.target.value)} placeholder="JP, US（空 = 不限）" />
        </label>
        <label className="adm-field">
          屏蔽地区
          <input value={f.region_deny} onChange={e => set('region_deny', e.target.value)} placeholder="CN" />
        </label>
      </div>
      <div className="adm-form-actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy}>
          保存片源
        </button>
        <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel}>
          取消
        </button>
      </div>
    </form>
  );
}

function PromoteForm({media, busy, onSubmit, onCancel}: {
  media: AdminMediaSource;
  busy: boolean;
  onSubmit: (v: {kind: string; label: string; version_kind: string; priority: number; license_ref: string; max_height: number | null; duration_seconds: number | null}) => void;
  onCancel: () => void;
}) {
  const [kind, setKind] = useState('external_link');
  const [label, setLabel] = useState(media.source);
  const [versionKind, setVersionKind] = useState('main');
  const [priority, setPriority] = useState('100');
  const [license, setLicense] = useState('');
  const [maxHeight, setMaxHeight] = useState('');
  const [duration, setDuration] = useState('');
  const needsLicense = requiresLicense(kind);
  return (
    <form
      className="adm-panel"
      onSubmit={e => {
        e.preventDefault();
        onSubmit({
          kind,
          label: label.trim(),
          version_kind: versionKind,
          priority: Number(priority) || 0,
          license_ref: license.trim(),
          max_height: optInt(maxHeight),
          duration_seconds: optInt(duration),
        });
      }}
    >
      <h3>转为片源 · {media.source}</h3>
      <p className="adm-field-hint mono">{media.media_url ?? media.media_id}</p>
      <div className="adm-form-grid">
        <label className="adm-field">
          类型
          <select value={kind} onChange={e => setKind(e.target.value)}>
            {SOURCE_KINDS.map(k => (
              <option key={k} value={k}>
                {KIND_LABELS[k]}
              </option>
            ))}
          </select>
        </label>
        <label className="adm-field">
          名称
          <input required value={label} maxLength={100} onChange={e => setLabel(e.target.value)} />
        </label>
        <label className="adm-field">
          版本
          <select value={versionKind} onChange={e => setVersionKind(e.target.value)}>
            {VERSION_KINDS.map(k => (
              <option key={k} value={k}>
                {VERSION_LABELS[k]}
              </option>
            ))}
          </select>
        </label>
        <label className="adm-field">
          优先级
          <input type="number" min={0} value={priority} onChange={e => setPriority(e.target.value)} />
        </label>
        <label className="adm-field">
          最高清晰度
          <input type="number" min={1} max={4320} value={maxHeight} onChange={e => setMaxHeight(e.target.value)} />
        </label>
        <label className="adm-field">
          时长（秒）
          <input type="number" min={1} value={duration} onChange={e => setDuration(e.target.value)} />
        </label>
        <label className="adm-field span-all">
          <span className="label-row">
            版权凭证 license_ref {needsLicense ? <Pill tone="amber">必填</Pill> : <Pill>可选</Pill>}
          </span>
          <input required={needsLicense} maxLength={200} value={license} onChange={e => setLicense(e.target.value)} />
        </label>
      </div>
      <div className="adm-form-actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy}>
          转为片源
        </button>
        <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel}>
          取消
        </button>
      </div>
    </form>
  );
}

export function AdminVideoSources({releaseId, sources: initial, media, onChanged}: {
  releaseId: number;
  sources: AdminPlaybackSource[];
  media: AdminMediaSource[];
  onChanged: () => void;
}) {
  const act = useAdminAction();
  const [list, setList] = useState(initial);
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [toggling, setToggling] = useState<number | null>(null);
  const [toggleReason, setToggleReason] = useState('');
  const [promoting, setPromoting] = useState<string | null>(null);
  const [dragId, setDragId] = useState<number | null>(null);
  const [overId, setOverId] = useState<number | null>(null);

  useEffect(() => setList(initial), [initial]);

  const saveOrder = async (ordered: AdminPlaybackSource[]) => {
    const before = list;
    setList(ordered);
    const res = await act.attempt('reorder', () => reorderReleaseSources(releaseId, ordered.map(s => s.id)), '已保存片源顺序');
    if (res) setList(res);
    else setList(before);
  };

  const move = (from: number, to: number) => {
    if (to < 0 || to >= list.length || from === to) return;
    const next = [...list];
    const [item] = next.splice(from, 1);
    next.splice(to, 0, item);
    void saveOrder(next);
  };

  const onDrop = (e: DragEvent, targetId: number) => {
    e.preventDefault();
    if (dragId === null) return;
    const from = list.findIndex(s => s.id === dragId);
    const to = list.findIndex(s => s.id === targetId);
    setDragId(null);
    setOverId(null);
    move(from, to);
  };

  const onKey = (e: KeyboardEvent, index: number) => {
    if (!e.altKey) return;
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      move(index, index - 1);
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      move(index, index + 1);
    }
  };

  const save = async (input: SourceInput) => {
    const ok =
      editing === 'new'
        ? await act.attempt('save', () => createReleaseSource(releaseId, input), '已添加片源')
        : await act.attempt('save', () => updateReleaseSource(releaseId, editing as number, input), '已保存片源');
    if (ok) {
      setEditing(null);
      onChanged();
    }
  };

  const toggle = async (s: AdminPlaybackSource) => {
    const enable = s.status !== 'active';
    const ok = await act.attempt(`toggle-${s.id}`, () => setReleaseSourceEnabled(releaseId, s.id, enable, toggleReason.trim()), enable ? '已启用片源' : '已停用片源');
    if (ok) {
      setToggling(null);
      setToggleReason('');
      onChanged();
    }
  };

  const promote = async (m: AdminMediaSource, v: Parameters<Parameters<typeof PromoteForm>[0]['onSubmit']>[0]) => {
    const ok = await act.attempt('promote', () => promoteMediaSource(releaseId, {source: m.source, ...v}), `已把 ${m.source} 转为片源`);
    if (ok) {
      setPromoting(null);
      onChanged();
    }
  };

  const editingSource = typeof editing === 'number' ? list.find(s => s.id === editing) : undefined;

  return (
    <div className="adm-card">
      <div className="adm-card-head">
        <div>
          <h2>片源（{list.length}）</h2>
          <p className="adm-field-hint">
            前台按顺序选默认片源，当前片源连续失败会自动切到下一个。拖动或在行上按 Alt+↑/↓ 调整顺序。
          </p>
        </div>
        <button type="button" className="btn btn-secondary btn-sm" onClick={() => setEditing(editing === 'new' ? null : 'new')}>
          + 添加片源
        </button>
      </div>
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />

      {editing === 'new' && <SourceForm initial={blank} title="添加片源" busy={act.pending === 'save'} onSubmit={i => void save(i)} onCancel={() => setEditing(null)} />}

      <div className="adm-sources" role="list" aria-label="片源顺序">
        {list.length === 0 && <p className="muted">还没有片源。</p>}
        {list.map((s, i) => {
          const off = s.status !== 'active';
          const healthOff = off && s.status_changed_by === 'health';
          return (
            <div key={s.id}>
              <div
                role="listitem"
                tabIndex={0}
                aria-label={`第 ${i + 1} 位：${s.label}`}
                className={`adm-source ${off ? 'off' : ''} ${dragId === s.id ? 'dragging' : ''} ${overId === s.id && dragId !== s.id ? 'drop-target' : ''}`}
                draggable
                onDragStart={e => {
                  setDragId(s.id);
                  e.dataTransfer.effectAllowed = 'move';
                }}
                onDragEnd={() => {
                  setDragId(null);
                  setOverId(null);
                }}
                onDragOver={e => {
                  e.preventDefault();
                  setOverId(s.id);
                }}
                onDrop={e => onDrop(e, s.id)}
                onKeyDown={e => onKey(e, i)}
              >
                <span className="adm-grip" aria-hidden="true">
                  <Icon name="grip" size={16} />
                </span>
                <span className="adm-source-rank">{i + 1}</span>
                <span className="adm-source-main">
                  <strong>
                    {s.label} {i === 0 && !off && <Pill tone="green">默认</Pill>}
                  </strong>
                  <span className="adm-source-meta">
                    {s.provider} · {KIND_LABELS[s.kind] ?? s.kind} · #{s.id}
                  </span>
                  <span className="adm-source-meta mono">{s.url ?? s.storage_prefix ?? ''}</span>
                </span>
                <span className="adm-chips">
                  <Pill tone={s.version_kind === 'main' ? 'plain' : s.version_kind === 'high_quality' ? 'blue' : s.version_kind === 'dub' ? 'green' : 'purple'}>
                    {VERSION_LABELS[s.version_kind] ?? s.version_kind}
                  </Pill>
                  {s.is_alt_version && s.version_kind !== 'alt' && <Pill tone="purple">其他版本</Pill>}
                  {s.max_height && (
                    <Pill>
                      {s.max_height}p{s.has_hdr ? ' HDR' : ''}
                    </Pill>
                  )}
                  {s.license_ref ? <Pill title={s.license_ref}>凭证 ✓</Pill> : requiresLicense(s.kind) ? <Pill tone="red">缺凭证</Pill> : null}
                </span>
                <span title={s.health_checked_at ? `评分于 ${fmtTime(s.health_checked_at)}` : '尚未评分'}>
                  <BandPill band={s.health_band} score={s.health_score} />
                </span>
                <span>
                  <Pill tone={statusTone(s.status)}>{s.status === 'active' ? '启用' : s.status === 'degraded' ? '降级' : '停用'}</Pill>
                  {healthOff && <div className="adm-source-meta">由健康检查自动处理</div>}
                </span>
                <span className="adm-source-actions">
                  <button type="button" className="adm-link" disabled={i === 0 || act.pending === 'reorder'} onClick={() => move(i, i - 1)} aria-label="上移">
                    ↑
                  </button>
                  <button
                    type="button"
                    className="adm-link"
                    disabled={i === list.length - 1 || act.pending === 'reorder'}
                    onClick={() => move(i, i + 1)}
                    aria-label="下移"
                  >
                    ↓
                  </button>
                  <button type="button" className="adm-link" onClick={() => setEditing(editing === s.id ? null : s.id)}>
                    编辑
                  </button>
                  <button
                    type="button"
                    className={`adm-link ${off ? '' : 'danger'}`}
                    onClick={() => {
                      setToggling(toggling === s.id ? null : s.id);
                      setToggleReason('');
                    }}
                  >
                    {off ? '启用' : '停用'}
                  </button>
                </span>
              </div>
              {toggling === s.id && (
                <form
                  className="adm-toolbar"
                  style={{padding: '8px 0 4px 66px'}}
                  onSubmit={e => {
                    e.preventDefault();
                    void toggle(s);
                  }}
                >
                  <input
                    autoFocus
                    value={toggleReason}
                    onChange={e => setToggleReason(e.target.value)}
                    required={healthOff}
                    maxLength={2000}
                    placeholder={healthOff ? '理由（必填：该片源由健康检查停用）' : '理由（可选）'}
                    style={{flex: 1, minWidth: 240}}
                  />
                  <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === `toggle-${s.id}`}>
                    确认{off ? '启用' : '停用'}
                  </button>
                  <button type="button" className="adm-link" onClick={() => setToggling(null)}>
                    取消
                  </button>
                </form>
              )}
              {editing === s.id && editingSource && (
                <SourceForm
                  initial={fromSource(editingSource)}
                  title={`编辑片源 · ${s.label}`}
                  busy={act.pending === 'save'}
                  onSubmit={input => void save(input)}
                  onCancel={() => setEditing(null)}
                />
              )}
            </div>
          );
        })}
      </div>

      <h3 style={{marginTop: 8}}>抓取到的播放来源（{media.length}）</h3>
      <p className="adm-field-hint">从站点抓到的播放页，确认版权后可以转为正式片源。</p>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>来源</th>
              <th>地址</th>
              <th>匹配</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {media.length === 0 && (
              <tr>
                <td colSpan={4} className="adm-empty">
                  没有抓取到的播放来源
                </td>
              </tr>
            )}
            {media.map(m => (
              <tr key={`${m.source}-${m.media_id}`}>
                <td>{m.source}</td>
                <td className="wrap mono">{m.media_url ?? m.media_id}</td>
                <td>
                  <Pill tone={m.confidence === 'exact' || m.confidence === 'high' ? 'green' : 'amber'}>{m.confidence}</Pill>
                </td>
                <td>
                  {m.promoted_source_id ? (
                    <span className="muted">已转为片源 #{m.promoted_source_id}</span>
                  ) : (
                    <button type="button" className="adm-link" onClick={() => setPromoting(promoting === m.source ? null : m.source)}>
                      转为片源
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {promoting && media.find(m => m.source === promoting) && (
        <PromoteForm
          key={promoting}
          media={media.find(m => m.source === promoting)!}
          busy={act.pending === 'promote'}
          onSubmit={v => void promote(media.find(m => m.source === promoting)!, v)}
          onCancel={() => setPromoting(null)}
        />
      )}
    </div>
  );
}
