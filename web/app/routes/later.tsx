import {useEffect, useRef, useState, type KeyboardEvent, type PointerEvent as ReactPointerEvent} from 'react';
import {Link, useNavigate} from 'react-router';
import {Thumb} from '../components/Cards';
import {Icon} from '../components/Icon';
import {CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getWatchLater, isMissingEndpoint, removeWatchLater, reorderWatchLater, type WatchLaterItem} from '../lib/api';
import {useAuth} from '../lib/auth';
import {formatDate} from '../lib/format';
import {startQueue} from '../lib/queue';
import {useSocial} from '../lib/social';
import {useAsync} from '../lib/useAsync';

export const meta = () => [{title: 'REEL · 稍后观看'}];

// NavLater: GET /api/me/watch-later in position order; drag the handle (mouse
// or touch) or focus it and press ↑ / ↓ to reorder, saved with POST
// /api/me/watch-later/reorder (the full order). 全部播放 / 随机播放 start a
// session queue the video page follows (lib/queue).
// TODO(PRD 7): 看完自动移出 and 移除已看完 need a server-side rule; 移到收藏夹
// waits for multiple favorite folders.

function totalLabel(items: WatchLaterItem[]): string {
  const minutes = items.reduce((sum, i) => sum + (i.runtime_minutes > 0 ? i.runtime_minutes : 0), 0);
  if (!minutes) return `${items.length} 个视频`;
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  const known = items.every(i => i.runtime_minutes > 0);
  return `${items.length} 个视频 · 共${known ? '' : '约'} ${h > 0 ? `${h} 小时 ` : ''}${m} 分`;
}

function shuffled<T>(list: T[]): T[] {
  const out = list.slice();
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out;
}

export default function LaterPage() {
  const {status} = useAuth();
  const {refreshLater} = useSocial();
  const navigate = useNavigate();
  const data = useAsync(signal => getWatchLater(signal), [status], status === 'user');
  const [items, setItems] = useState<WatchLaterItem[]>([]);
  const [error, setError] = useState('');
  const [announce, setAnnounce] = useState('');
  const [dragging, setDragging] = useState<string | null>(null);
  const listRef = useRef<HTMLOListElement>(null);
  const committed = useRef<string[]>([]);
  const drag = useRef<{slug: string; pointer: number; startOrder: string[]} | null>(null);

  useEffect(() => {
    if (data.data) {
      setItems(data.data);
      committed.current = data.data.map(i => i.slug);
    }
  }, [data.data]);

  if (status === 'anon') {
    return (
      <div className="page">
        <EmptyState icon="later" title="登录后使用稍后观看" body="看到想看的先存下来，换设备也能接着看。" actions={[{to: '/login?next=%2Flater', label: '登录'}]} />
      </div>
    );
  }

  const save = async (next: WatchLaterItem[]) => {
    const order = next.map(i => i.slug);
    if (order.join('\n') === committed.current.join('\n')) return;
    const previous = committed.current;
    committed.current = order;
    setError('');
    try {
      await reorderWatchLater(order);
    } catch (cause) {
      committed.current = previous;
      setItems(cur => previous.map(slug => cur.find(i => i.slug === slug)).filter((i): i is WatchLaterItem => !!i));
      setError(cause instanceof Error ? `排序没有保存：${cause.message}` : '排序没有保存');
    }
  };

  const move = (from: number, to: number): WatchLaterItem[] => {
    const next = items.slice();
    const [it] = next.splice(from, 1);
    next.splice(to, 0, it);
    setItems(next);
    return next;
  };

  const onHandleKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const delta = event.key === 'ArrowUp' ? -1 : event.key === 'ArrowDown' ? 1 : 0;
    const to = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : index + delta;
    if ((delta === 0 && event.key !== 'Home' && event.key !== 'End') || to < 0 || to >= items.length || to === index) return;
    event.preventDefault();
    const target = event.currentTarget;
    const next = move(index, to);
    setAnnounce(`《${items[index].title}》移到第 ${to + 1} 位，共 ${items.length} 个`);
    save(next);
    // Keep focus on the moved row's handle after React re-renders.
    window.requestAnimationFrame(() => {
      const rows = listRef.current?.querySelectorAll<HTMLButtonElement>('.drag-handle');
      (rows?.[to] ?? target).focus();
    });
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLButtonElement>, slug: string) => {
    if (event.button !== 0) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = {slug, pointer: event.pointerId, startOrder: items.map(i => i.slug)};
    setDragging(slug);
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d || d.pointer !== event.pointerId || !listRef.current) return;
    const rows = Array.from(listRef.current.querySelectorAll<HTMLElement>('.later-item'));
    const from = items.findIndex(i => i.slug === d.slug);
    let to = from;
    for (let i = 0; i < rows.length; i++) {
      const rect = rows[i].getBoundingClientRect();
      if (event.clientY < rect.top + rect.height / 2) {
        to = i > from ? i - 1 : i;
        break;
      }
      to = i;
    }
    if (to !== from && to >= 0) move(from, to);
  };

  const onPointerUp = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d || d.pointer !== event.pointerId) return;
    drag.current = null;
    setDragging(null);
    const index = items.findIndex(i => i.slug === d.slug);
    if (items.map(i => i.slug).join('\n') !== d.startOrder.join('\n')) {
      setAnnounce(`《${items[index]?.title ?? ''}》移到第 ${index + 1} 位`);
      save(items);
    }
  };

  const remove = async (item: WatchLaterItem) => {
    setError('');
    const before = items;
    setItems(before.filter(i => i.slug !== item.slug));
    committed.current = committed.current.filter(s => s !== item.slug);
    try {
      await removeWatchLater(item.slug);
      refreshLater();
    } catch (cause) {
      setItems(before);
      committed.current = before.map(i => i.slug);
      setError(cause instanceof Error ? cause.message : '移除失败');
    }
  };

  const play = (order: WatchLaterItem[]) => {
    if (order.length === 0) return;
    startQueue('later', order.map(i => i.slug));
    navigate(`/releases/${encodeURIComponent(order[0].slug)}?list=later`);
  };

  return (
    <div className="page narrow later-page">
      <div className="later-head">
        <div>
          <h1 className="page-title">稍后观看</h1>
          {items.length > 0 && <p className="muted">{totalLabel(items)} · 仅自己可见</p>}
        </div>
        {items.length > 0 && (
          <div className="action-row">
            <button type="button" className="btn btn-primary" onClick={() => play(items)}>
              <Icon name="play" size={16} filled />
              全部播放
            </button>
            <button type="button" className="btn btn-secondary" onClick={() => play(shuffled(items))}>
              <Icon name="shuffle" size={16} />
              随机播放
            </button>
          </div>
        )}
      </div>
      {error && <ErrorBanner error={error} />}
      <span className="sr-only" role="status" aria-live="polite">
        {announce}
      </span>
      {data.error ? (
        isMissingEndpoint(data.error) ? (
          <EmptyState icon="later" title="稍后观看即将上线" body="上线后可以把想看的视频先存下来。" />
        ) : (
          <ErrorBanner error={data.error} onRetry={data.reload} />
        )
      ) : data.loading ? (
        data.slow && <CardGridSkeleton count={5} variant="row" />
      ) : items.length === 0 ? (
        <EmptyState icon="later" title="稍后观看还是空的" body="在视频卡片或视频页点「稍后观看」，作品会出现在这里。" actions={[{to: '/', label: '去首页逛逛'}]} />
      ) : (
        <>
          <p className="muted later-hint" id="later-hint">
            拖动左侧手柄调整顺序；键盘用户可以聚焦手柄后按 ↑ / ↓。
          </p>
          <ol className="later-list" ref={listRef}>
            {items.map((item, index) => (
              <li key={item.slug} className={dragging === item.slug ? 'later-item is-dragging' : 'later-item'}>
                <button
                  type="button"
                  className="drag-handle"
                  aria-label={`调整《${item.title}》的顺序，当前第 ${index + 1} 位`}
                  aria-describedby="later-hint"
                  onKeyDown={event => onHandleKey(event, index)}
                  onPointerDown={event => onPointerDown(event, item.slug)}
                  onPointerMove={onPointerMove}
                  onPointerUp={onPointerUp}
                  onPointerCancel={onPointerUp}
                >
                  <Icon name="grip" />
                </button>
                <span className="later-index" aria-hidden="true">
                  {index + 1}
                </span>
                <Link to={`/releases/${encodeURIComponent(item.slug)}`} className="release-row-link">
                  <Thumb seed={item.slug} badge={item.runtime_minutes ? `${item.runtime_minutes} 分` : undefined} className="thumb-row" />
                  <span className="release-row-text">
                    <span className="card-title">{item.title}</span>
                    <span className="card-meta">加入于 {formatDate(item.added_at)}</span>
                  </span>
                </Link>
                <button type="button" className="icon-btn" aria-label={`从稍后观看移除《${item.title}》`} title="移除" onClick={() => remove(item)}>
                  <Icon name="close" size={18} />
                </button>
              </li>
            ))}
          </ol>
        </>
      )}
    </div>
  );
}
