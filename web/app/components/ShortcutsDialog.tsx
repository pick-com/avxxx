import {useEffect, useRef} from 'react';
import {createPortal} from 'react-dom';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Icon} from './Icon';

type Row = {label: MessageKey; vars?: Record<string, string | number>; keys: string[][]};

const IS_MAC = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const ALT = IS_MAC ? 'Option' : 'Alt';

// Each row lists alternative key combos; each combo is a list of keycaps.
const SECTIONS: {title: MessageKey; rows: Row[]}[] = [
  {
    title: 'pc.keys.playback',
    rows: [
      {label: 'pc.keys.playPause', keys: [['K'], ['Space']]},
      {label: 'pc.keys.back', vars: {s: 10}, keys: [['J']]},
      {label: 'pc.keys.forward', vars: {s: 10}, keys: [['L']]},
      {label: 'pc.keys.back', vars: {s: 5}, keys: [['←']]},
      {label: 'pc.keys.forward', vars: {s: 5}, keys: [['→']]},
      {label: 'pc.keys.prevVideo', keys: [['Shift', 'P']]},
      {label: 'pc.keys.nextVideo', keys: [['Shift', 'N']]},
      {label: 'pc.keys.prevFrame', keys: [[',']]},
      {label: 'pc.keys.nextFrame', keys: [['.']]},
      {label: 'pc.keys.slower', keys: [['<']]},
      {label: 'pc.keys.faster', keys: [['>']]},
      {label: 'pc.keys.seekPercent', keys: [['0'], ['…'], ['9']]},
      {label: 'pc.keys.toStart', keys: [['Home']]},
      {label: 'pc.keys.toEnd', keys: [['End']]},
      {label: 'pc.keys.prevChapter', keys: [[ALT, '←']]},
      {label: 'pc.keys.nextChapter', keys: [[ALT, '→']]},
    ],
  },
  {
    title: 'pc.keys.general',
    rows: [
      {label: 'pc.keys.fullscreen', keys: [['F']]},
      {label: 'pc.keys.theater', keys: [['T']]},
      {label: 'pc.keys.pip', keys: [['I']]},
      {label: 'pc.keys.escape', keys: [['Esc']]},
      {label: 'pc.keys.mute', keys: [['M']]},
      {label: 'pc.keys.volume', keys: [['↑'], ['↓']]},
      {label: 'pc.keys.help', keys: [['?']]},
    ],
  },
  {
    title: 'pc.keys.captions',
    rows: [{label: 'pc.keys.toggleCaptions', keys: [['C']]}],
  },
];

/**
 * YouTube-style keyboard shortcut reference, opened with `?` in the player.
 * Rendered into `container` (the player shell in fullscreen) or the body.
 */
export function ShortcutsDialog({open, onClose, container}: {open: boolean; onClose: () => void; container?: HTMLElement | null}) {
  const t = useT();
  const dialogRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    dialogRef.current?.focus({preventScroll: true});
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' || event.key === '?') {
        event.preventDefault();
        event.stopPropagation();
        onCloseRef.current();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      previous?.focus({preventScroll: true});
    };
  }, [open]);

  if (!open || typeof document === 'undefined') return null;
  return createPortal(
    <div className="shortcuts-backdrop" onClick={onClose}>
      <div
        ref={dialogRef}
        className="shortcuts-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="shortcuts-title"
        tabIndex={-1}
        onClick={event => event.stopPropagation()}
      >
        <header className="shortcuts-head">
          <h2 id="shortcuts-title">{t('pc.keys.title')}</h2>
          <button type="button" className="icon-btn" aria-label={t('pc.keys.close')} onClick={onClose}>
            <Icon name="close" size={20} />
          </button>
        </header>
        <div className="shortcuts-body">
          {SECTIONS.map(section => (
            <section key={section.title}>
              <h3>{t(section.title)}</h3>
              <dl>
                {section.rows.map((row, i) => (
                  <div className="shortcuts-row" key={i}>
                    <dt>{t(row.label, row.vars)}</dt>
                    <dd>
                      {row.keys.map((combo, j) => (
                        <span className="shortcuts-combo" key={j}>
                          {combo.map((cap, k) => (
                            <kbd key={k}>{cap}</kbd>
                          ))}
                        </span>
                      ))}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </div>,
    container ?? document.body,
  );
}
