import type {ReactNode} from 'react';
import {Link} from 'react-router';
import {useT} from '../lib/i18n';
import {Icon, type IconName} from './Icon';

/** States 05-style banner: what happened + a retry. */
export function ErrorBanner({error, onRetry, children}: {error: Error | string; onRetry?: () => void; children?: ReactNode}) {
  const t = useT();
  const message = typeof error === 'string' ? error : error.message;
  return (
    <div className="banner banner-warn" role="alert">
      <Icon name="alert" size={18} />
      <span className="banner-text">{children ?? message}</span>
      {onRetry && (
        <button type="button" className="banner-action" onClick={onRetry}>
          {t('common.retry')}
        </button>
      )}
    </div>
  );
}

type EmptyAction = {to: string; label: string; primary?: boolean};

/** States 01/02: explain what is missing and give one next step. */
export function EmptyState({icon = 'empty', title, body, actions = [], children}: {
  icon?: IconName;
  title: string;
  body?: ReactNode;
  actions?: EmptyAction[];
  children?: ReactNode;
}) {
  return (
    <section className="empty-state">
      <span className="empty-icon">
        <Icon name={icon} size={36} />
      </span>
      <h2>{title}</h2>
      {body && <p>{body}</p>}
      {actions.length > 0 && (
        <div className="empty-actions">
          {actions.map(a => (
            <Link key={a.to + a.label} to={a.to} className={a.primary === false ? 'btn btn-secondary' : 'btn btn-primary'}>
              {a.label}
            </Link>
          ))}
        </div>
      )}
      {children}
    </section>
  );
}

/** Skeleton shaped like the real cards (States 03). Render only when `slow`. */
export function CardGridSkeleton({count = 10, variant = 'video'}: {count?: number; variant?: 'video' | 'avatar' | 'row'}) {
  const t = useT();
  return (
    <div className={variant === 'avatar' ? 'avatar-row' : variant === 'row' ? 'row-list' : 'card-grid'} aria-busy="true" aria-label={t('app.loading')}>
      {Array.from({length: count}, (_, i) => (
        <div key={i} className={`skeleton-card skeleton-${variant}`}>
          <span className="sk sk-media" />
          <span className="sk-lines">
            <span className="sk sk-line" style={{width: '88%'}} />
            <span className="sk sk-line sk-line-2" style={{width: `${40 + ((i * 13) % 35)}%`}} />
          </span>
        </div>
      ))}
    </div>
  );
}

/** Text skeleton block for detail headers. */
export function BlockSkeleton({lines = 3}: {lines?: number}) {
  const t = useT();
  return (
    <div className="sk-block" aria-busy="true" aria-label={t('app.loading')}>
      {Array.from({length: lines}, (_, i) => (
        <span key={i} className="sk sk-line" style={{width: `${90 - i * 18}%`}} />
      ))}
    </div>
  );
}
