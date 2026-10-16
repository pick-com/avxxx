import {createContext, useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ComponentType} from 'react';
import {createPortal} from 'react-dom';
import {useLocation, useNavigate} from '../lib/nav';
import {formatClock} from '../lib/format';
import {useT} from '../lib/i18n';
import {useHydrated} from '../lib/useHydrated';
import type {PlayerProps} from './Player';
import {Icon} from './Icon';

// One player instance for the whole app (PRD 9 迷你播放器, MobileHome M1).
//
// The release page does not render <Player> itself: it publishes the Player
// component and its props here and leaves an empty slot. The shell renders
// the Player through a portal into one persistent container element that is
// moved (appendChild, same task, so the <video> keeps playing) into:
//   - the page's slot while the release page is mounted;
//   - the mini bar above the bottom tabs when a phone leaves the release
//     page while the video plays (title, play / pause, close, tap to return);
//   - an invisible parking spot when a desktop leaves it in picture-in-
//     picture, until PiP is closed.
// Otherwise leaving the page unmounts the Player as before (play_end, save).
// The Player module stays in the release route's chunk: the shell only holds
// a reference to the component the page published.

type Mode = 'off' | 'page' | 'mini' | 'pip';
type State = {Component: ComponentType<PlayerProps> | null; props: PlayerProps | null; mode: Mode; href: string};

const MOBILE = '(max-width: 767px)';

class HostStore {
  state: State = {Component: null, props: null, mode: 'off', href: ''};
  readonly el: HTMLDivElement | null;
  private slot: HTMLElement | null = null;
  miniFrame: HTMLElement | null = null;
  parking: HTMLElement | null = null;
  private listeners = new Set<() => void>();

  constructor() {
    this.el = typeof document === 'undefined' ? null : document.createElement('div');
    if (this.el) this.el.className = 'player-host';
  }

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  get = () => this.state;
  private set(patch: Partial<State>) {
    this.state = {...this.state, ...patch};
    if (this.el) this.el.dataset.mode = this.state.mode;
    this.listeners.forEach(fn => fn());
  }

  publish(Component: ComponentType<PlayerProps>, props: PlayerProps, href: string) {
    this.set({Component, props, href, mode: 'page'});
  }

  attach(slot: HTMLElement) {
    if (!this.el) return;
    this.slot = slot;
    if (this.el.parentNode !== slot) slot.appendChild(this.el);
    if (this.state.mode !== 'page' && this.state.props) this.set({mode: 'page'});
  }

  /** The release page goes away: keep playing in the mini bar / PiP, or stop. */
  release(slot: HTMLElement) {
    if (!this.el || this.slot !== slot) return;
    this.slot = null;
    const video = this.el.querySelector('video');
    const playing = !!video && !video.paused && !video.ended;
    const pip = typeof document !== 'undefined' && !!document.pictureInPictureElement && this.el.contains(document.pictureInPictureElement);
    const props = this.state.props;
    if (props && playing && this.miniFrame && window.matchMedia(MOBILE).matches) {
      this.miniFrame.appendChild(this.el);
      this.set({mode: 'mini', props: detached(props)});
    } else if (props && pip && this.parking) {
      this.parking.appendChild(this.el);
      this.set({mode: 'pip', props: detached(props)});
    } else {
      this.close();
    }
  }

  close() {
    this.el?.remove();
    this.set({Component: null, props: null, mode: 'off'});
  }
}

/** Page-only callbacks and overlays do not follow the player out of the page. */
const detached = (props: PlayerProps): PlayerProps => ({
  ...props,
  onBack: undefined,
  onToggleTheater: undefined,
  onEnded: undefined,
  onReplay: undefined,
  overlay: null,
  related: [],
  episodes: undefined,
  theater: false,
  autoStart: false,
});

const HostContext = createContext<HostStore | null>(null);

export function usePlayerHostStore() {
  return useState(() => new HostStore())[0];
}

export const PlayerHostProvider = HostContext.Provider;

/** True while the mini bar is showing (the shell adds room for it). */
export function useMiniActive(): boolean {
  const store = useContext(HostContext);
  return useSyncExternalStore(
    store?.subscribe ?? noopSubscribe,
    () => store?.state.mode === 'mini',
    () => false,
  );
}
const noopSubscribe = () => () => undefined;

/** Used by the release page in place of <Player>. */
export function HostedPlayer({component: Component, ...props}: {component: ComponentType<PlayerProps>} & PlayerProps) {
  const store = useContext(HostContext);
  const slotRef = useRef<HTMLDivElement>(null);
  const location = useLocation();
  const href = location.pathname + location.search;
  useLayoutEffect(() => {
    store?.publish(Component, props, href);
  });
  useLayoutEffect(() => {
    const slot = slotRef.current;
    if (!store || !slot) return;
    store.attach(slot);
    return () => store.release(slot);
  }, [store]);
  if (!store) return <Component {...props} />;
  return <div ref={slotRef} className="player-slot" />;
}

/** Shell part: the portal, the mini bar and the PiP parking spot. */
export function PlayerHostLayer() {
  const store = useContext(HostContext);
  const state = useSyncExternalStore(store?.subscribe ?? noopSubscribe, () => store?.state ?? EMPTY, () => EMPTY);
  // Browser-only (portal target, video element): never part of the server HTML.
  const hydrated = useHydrated();
  if (!hydrated || !store || !store.el) return null;
  const {Component, props, mode} = state;
  return (
    <>
      {Component && props && createPortal(<Component {...props} mini={mode === 'mini'} />, store.el)}
      <div
        className="player-parking"
        aria-hidden="true"
        ref={el => {
          store.parking = el;
        }}
      />
      <MiniBar store={store} active={mode === 'mini' || mode === 'pip'} visible={mode === 'mini'} title={props?.title ?? ''} href={state.href} />
    </>
  );
}
const EMPTY: State = {Component: null, props: null, mode: 'off', href: ''};

function useHostedVideo(store: HostStore, active: boolean) {
  const [video, setVideo] = useState<HTMLVideoElement | null>(null);
  useEffect(() => {
    if (!active || !store.el) {
      setVideo(null);
      return;
    }
    const find = () => setVideo(store.el?.querySelector('video') ?? null);
    find();
    // A source switch renders a new <video>.
    const observer = new MutationObserver(find);
    observer.observe(store.el, {childList: true, subtree: true});
    return () => observer.disconnect();
  }, [store, active]);
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!video) return;
    const bump = () => setTick(n => n + 1);
    const events = ['play', 'pause', 'ended', 'timeupdate', 'durationchange'];
    events.forEach(e => video.addEventListener(e, bump));
    return () => events.forEach(e => video.removeEventListener(e, bump));
  }, [video]);
  return video;
}

function MiniBar({store, active, visible, title, href}: {store: HostStore; active: boolean; visible: boolean; title: string; href: string}) {
  const t = useT();
  const navigate = useNavigate();
  const video = useHostedVideo(store, active);

  // Desktop PiP parking: closing the PiP window ends the playback.
  useEffect(() => {
    if (!video || store.state.mode !== 'pip') return;
    const onLeave = () => {
      if (store.state.mode === 'pip') store.close();
    };
    video.addEventListener('leavepictureinpicture', onLeave);
    return () => video.removeEventListener('leavepictureinpicture', onLeave);
  }, [video, store, active]);

  const time = video?.currentTime ?? 0;
  const duration = video && Number.isFinite(video.duration) ? video.duration : 0;
  const paused = !video || video.paused;
  return (
    <section className="mini-player" hidden={!visible} aria-label={t('mini.label')}>
      <span className="mini-progress" aria-hidden="true">
        <span style={{transform: `scaleX(${duration ? Math.min(1, time / duration) : 0})`}} />
      </span>
      <button type="button" className="mini-open" aria-label={t('mini.expand', {title})} onClick={() => href && navigate(href)}>
        <span
          className="mini-frame"
          ref={el => {
            store.miniFrame = el;
          }}
        />
        <span className="mini-text">
          <span className="mini-title">{title}</span>
          <span className="mini-time">
            {formatClock(time)} / {formatClock(duration)}
          </span>
        </span>
      </button>
      <button
        type="button"
        className="mini-btn"
        aria-label={paused ? t('player.play') : t('player.pause')}
        onClick={() => {
          if (!video) return;
          if (video.paused) void video.play().catch(() => undefined);
          else video.pause();
        }}
      >
        <Icon name={paused ? 'play' : 'pause'} size={22} filled={paused} />
      </button>
      <button type="button" className="mini-btn" aria-label={t('mini.close')} onClick={() => store.close()}>
        <Icon name="close" size={20} />
      </button>
    </section>
  );
}
