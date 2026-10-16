import {useEffect, useState} from 'react';
import {Link} from '../lib/nav';
import {useT} from '../lib/i18n';
import {formatDate, formatRuntime, swatch} from '../lib/format';
import type {HomeSlot} from '../lib/home';
import {CatalogImage} from './Cards';
import {Icon} from './Icon';
import '../styles/home-ops.css';

const ROTATE_MS = 7000;

/**
 * 主推横幅 (NavHome / MobileHome, PRD 4.1): the live hero slots from GET
 * /api/home/slots, or recommendation fill-ins when none is scheduled. The
 * frame has a fixed aspect ratio and the first cover loads eagerly, so the
 * server-rendered banner does not shift the page. Several slots rotate
 * every 7 s (paused on hover / focus and with reduced motion).
 */
export function HomeHero({slots, fill = false}: {slots: HomeSlot[]; fill?: boolean}) {
  const t = useT();
  const [index, setIndex] = useState(0);
  const [paused, setPaused] = useState(false);
  const count = slots.length;

  useEffect(() => {
    if (count < 2 || paused) return;
    if (typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    const timer = window.setInterval(() => setIndex(i => (i + 1) % count), ROTATE_MS);
    return () => window.clearInterval(timer);
  }, [count, paused]);

  if (count === 0) return null;
  const at = Math.min(index, count - 1);

  return (
    <section
      className="home-hero"
      aria-roledescription="carousel"
      aria-label={t('home.hero')}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <div className="home-hero-frame">
        {slots.map((s, i) => (
          <HeroPanel key={s.id} slot={s} active={i === at} eager={i === 0} fill={fill} position={i + 1} />
        ))}
      </div>
      {count > 1 && (
        <div className="home-hero-dots" role="tablist" aria-label={t('home.hero')}>
          {slots.map((s, i) => (
            <button
              key={s.id}
              type="button"
              role="tab"
              aria-selected={i === at}
              aria-label={t('home.heroGoTo', {n: i + 1, title: s.title})}
              className={`home-hero-dot${i === at ? ' is-active' : ''}`}
              onClick={() => setIndex(i)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function HeroPanel({slot, active, eager, fill, position}: {slot: HomeSlot; active: boolean; eager: boolean; fill: boolean; position: number}) {
  const t = useT();
  const r = slot.release;
  const meta = slot.series
    ? t('home.heroSeriesCount', {n: slot.series.release_count})
    : r
      ? [r.maker_name, formatDate(r.release_date), formatRuntime(r.runtime_minutes)].filter(Boolean).join(' · ')
      : '';
  const cta = slot.target_type === 'release' ? t('home.heroWatch') : slot.target_type === 'series' ? t('home.heroSeries') : t('home.heroOpen');
  const seed = r?.slug ?? slot.series?.slug ?? String(slot.id);
  const state = {slot: `home:hero:c${position}`};
  const body = (
    <>
      {slot.cover ? (
        <CatalogImage image={slot.cover} className="home-hero-img" sizes="(max-width: 767px) 100vw, 1200px" eager={eager} alt="" />
      ) : (
        <span className={`home-hero-img home-hero-ph ph-${swatch(seed)}`} aria-hidden="true">
          <Icon name="film" size={40} />
        </span>
      )}
      <span className="home-hero-shade" aria-hidden="true" />
      <span className="home-hero-text">
        <span className="home-hero-eyebrow">{fill ? t('home.heroFill') : t('home.heroPick')}</span>
        <span className="home-hero-title">{slot.title}</span>
        {slot.subtitle && <span className="home-hero-sub">{slot.subtitle}</span>}
        {meta && <span className="home-hero-meta">{meta}</span>}
        <span className="btn btn-primary home-hero-cta">
          <Icon name={slot.target_type === 'link' && slot.external ? 'external' : 'play'} size={16} />
          {cta}
        </span>
      </span>
    </>
  );
  const props = {className: 'home-hero-panel', 'aria-hidden': !active, tabIndex: active ? undefined : -1, 'data-active': active ? 'true' : 'false'};
  if (slot.external) {
    return (
      <a href={slot.href} target="_blank" rel="noopener noreferrer" {...props}>
        {body}
      </a>
    );
  }
  return (
    <Link to={slot.href} state={state} {...props}>
      {body}
    </Link>
  );
}
