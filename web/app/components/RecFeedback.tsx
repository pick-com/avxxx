import {useEffect, useState, useSyncExternalStore, type ReactNode} from 'react';
import {useT} from '../lib/i18n';
import {postErrorMessage} from '../lib/community';
import {addRecFeedback, listRecFeedback, undoRecFeedback, type RecFeedback, type RecFeedbackKind} from '../lib/recFeedback';
import {recsMissing, recTitle, type RecItem} from '../lib/recs';
import type {MessageKey} from '../lib/messages.zh-CN';
import {CardMenu, useReleaseMenuItems, type CardMenuItem} from './CardMenu';

// 推荐反馈 on recommendation cards (home shelves, 接下来播放) and in Settings
// (internal/api/rec_feedback.go). Picking 「不感兴趣」 or 「不推荐此演员」 in a
// card's 「⋯」 menu collapses the card to 「已隐藏 · 撤销」; hiding a performer
// collapses every card on the page they lead. Cards carry no tags, so
// 「不推荐此类型」 has no card entry (the API and Settings handle tag rows).

type Hidden = {kind: RecFeedbackKind; target: string};

// Feedback given on this page load, keyed "kind:target".
const hidden = new Map<string, Hidden>();
const listeners = new Set<() => void>();
let version = 0;
const keyOf = (kind: RecFeedbackKind, target: string) => `${kind}:${target}`;

function setHidden(h: Hidden, on: boolean) {
  if (on) hidden.set(keyOf(h.kind, h.target), h);
  else hidden.delete(keyOf(h.kind, h.target));
  version++;
  listeners.forEach(l => l());
}

function useHiddenVersion() {
  return useSyncExternalStore(
    cb => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => version,
    () => 0,
  );
}

/** What hides this item, if anything: its own 不感兴趣, else its lead performer's 不推荐. */
function hiddenBy(item: RecItem): Hidden | undefined {
  return hidden.get(keyOf('not_interested_release', item.slug)) ?? (item.performer ? hidden.get(keyOf('not_interested_performer', item.performer.slug)) : undefined);
}

/**
 * A recommended release with the 推荐反馈 menu. children renders the card
 * (ReleaseCard / ReleaseRow) given the menu element to place in it.
 */
export function RecFeedbackCard({item, variant, children}: {item: RecItem; variant: 'card' | 'row'; children: (menu: ReactNode) => ReactNode}) {
  const t = useT();
  useHiddenVersion();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const by = hiddenBy(item);
  const shared = useReleaseMenuItems(item.slug, recTitle(item));

  const give = async (h: Hidden) => {
    setError('');
    setHidden(h, true); // optimistic
    try {
      await addRecFeedback(h.kind, h.target);
    } catch (cause) {
      setHidden(h, false);
      setError(postErrorMessage(cause));
    }
  };

  const undo = async (h: Hidden) => {
    setBusy(true);
    setError('');
    try {
      await undoRecFeedback(h.kind, h.target);
      setHidden(h, false);
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  if (by) {
    return (
      <div className={`rec-hidden rec-hidden-${variant}`} role="group" aria-label={recTitle(item)}>
        <span className="rec-hidden-text">{t('recFeedback.hidden')}</span>
        <span aria-hidden="true">·</span>
        <button type="button" className="text-btn" disabled={busy} onClick={() => void undo(by)}>
          {t('recFeedback.undo')}
        </button>
        {error && (
          <span className="field-error" role="alert">
            {error}
          </span>
        )}
      </div>
    );
  }

  const items: CardMenuItem[] = [...shared, {key: 'not-interested', label: t('recFeedback.notInterested'), onSelect: () => void give({kind: 'not_interested_release', target: item.slug})}];
  if (item.performer) {
    const target = item.performer.slug;
    items.push({key: 'not-performer', label: t('recFeedback.notPerformer'), onSelect: () => void give({kind: 'not_interested_performer', target})});
  }
  const menu = <CardMenu label={t('recFeedback.menuLabel', {title: recTitle(item)})} items={items} />;
  return (
    <>
      {children(menu)}
      {error && (
        <span className="field-error rec-feedback-error" role="alert">
          {error}
        </span>
      )}
    </>
  );
}

const KIND_LABELS: Record<RecFeedbackKind, MessageKey> = {
  not_interested_release: 'settings.recFeedbackRelease',
  not_interested_performer: 'settings.recFeedbackPerformer',
  not_interested_tag: 'settings.recFeedbackTag',
};

/** Settings › 隐私 › 推荐反馈: the active feedback, newest first, each with 撤销. */
export function RecFeedbackSettings() {
  const t = useT();
  const [items, setItems] = useState<RecFeedback[] | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');

  useEffect(() => {
    const ctrl = new AbortController();
    listRecFeedback(ctrl.signal)
      .then(setItems)
      .catch(cause => {
        if (ctrl.signal.aborted) return;
        // A backend without the routes hides the row.
        setError(recsMissing(cause) ? 'missing' : postErrorMessage(cause));
      });
    return () => ctrl.abort();
  }, []);

  if (error === 'missing') return null;

  const undo = async (f: RecFeedback) => {
    const key = keyOf(f.kind, f.target);
    setBusy(key);
    setError('');
    try {
      await undoRecFeedback(f.kind, f.target);
      setItems(cur => (cur ?? []).filter(x => keyOf(x.kind, x.target) !== key));
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy('');
    }
  };

  return (
    <div className="set-row set-row-stack">
      <div className="set-text">
        <span className="set-label">{t('settings.recFeedback')}</span>
        <span className="set-desc">{t('settings.recFeedbackDesc')}</span>
        {error && (
          <span className="field-error" role="alert">
            {error}
          </span>
        )}
      </div>
      {items && items.length === 0 && <span className="set-desc">{t('settings.recFeedbackEmpty')}</span>}
      {items && items.length > 0 && (
        <ul className="rec-feedback-list">
          {items.map(f => {
            const key = keyOf(f.kind, f.target);
            return (
              <li key={key} className="rec-feedback-item">
                <span className="rec-feedback-kind">{t(KIND_LABELS[f.kind] ?? 'settings.recFeedbackRelease')}</span>
                <span className="rec-feedback-name">{f.name}</span>
                <button type="button" className="text-btn" disabled={busy === key} onClick={() => void undo(f)}>
                  {t('recFeedback.undo')}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
