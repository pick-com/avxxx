import {Link, useNavigate} from '../lib/nav';
import {Icon} from '../components/Icon';
import {EmptyState} from '../components/States';
import {getPlaylists} from '../lib/api';
import {useAuth} from '../lib/auth';
import {t, useT} from '../lib/i18n';
import {playlistName, visibilityIcon} from '../lib/playlists';
import {themeLabels, useTheme, type ThemePref} from '../lib/theme';
import {useAsync} from '../lib/useAsync';
import '../styles/library.css';

export const meta = () => [{title: t('title.me')}];

// MobileLibrary (我的 tab): account, library links, 我的收藏夹 (each opens
// /favorites?id=), appearance.
export default function Me() {
  const t = useT();
  const {status, user, signOut} = useAuth();
  const {pref, setPref} = useTheme();
  const navigate = useNavigate();
  const playlists = useAsync(signal => getPlaylists(undefined, signal), [status], status === 'user');

  return (
    <div className="page me-page">
      {status === 'anon' && (
        <EmptyState
          icon="me"
          title={t('me.anonTitle')}
          body={t('me.anonBody')}
          actions={[{to: '/login?next=%2Fme', label: t('home.loginRegister')}]}
        />
      )}
      {status === 'user' && user && (
        <>
          <section className="account-card">
            <span className="avatar-btn avatar-lg">
              <span className="avatar-initial">{user.email.slice(0, 1).toUpperCase()}</span>
            </span>
            <span className="account-email">{user.email}</span>
          </section>
          <nav className="list-links" aria-label={t('me.library')}>
            <Link to="/history" className="list-link">
              <Icon name="history" />
              {t('nav.history')}
            </Link>
            <Link to="/favorites" className="list-link">
              <Icon name="favorites" />
              {t('nav.favorites')}
            </Link>
            <Link to="/later" className="list-link">
              <Icon name="later" />
              {t('nav.later')}
            </Link>
            <Link to="/reservations" className="list-link">
              <Icon name="calendar" />
              {t('me.reservations')}
            </Link>
            <Link to="/notifications" className="list-link">
              <Icon name="bell" />
              {t('me.notifications')}
            </Link>
            <Link to="/settings" className="list-link">
              <Icon name="settings" />
              {t('settings.title')}
            </Link>
          </nav>
          {playlists.data && playlists.data.length > 0 && (
            <section className="shelf">
              <h2 className="section-title">{t('playlists.mine')}</h2>
              <nav className="list-links" aria-label={t('playlists.mine')}>
                {playlists.data.map(p => (
                  <Link key={p.id} to={`/favorites?id=${p.id}`} className="list-link">
                    <Icon name={visibilityIcon[p.visibility]} />
                    <span className="lib-link-name">{playlistName(p)}</span>
                    <span className="chip-count">{p.item_count}</span>
                  </Link>
                ))}
              </nav>
            </section>
          )}
        </>
      )}

      <section className="shelf">
        <h2 className="section-title">{t('settings.appearance')}</h2>
        <div className="segmented" role="radiogroup" aria-label={t('settings.appearance')}>
          {(['system', 'dark', 'light'] as ThemePref[]).map(p => (
            <button key={p} type="button" role="radio" aria-checked={pref === p} className="segmented-item" onClick={() => setPref(p)}>
              {themeLabels[p]}
            </button>
          ))}
        </div>
      </section>

      {status === 'user' && (
        <button
          type="button"
          className="btn btn-secondary btn-block"
          onClick={() => {
            signOut();
            navigate('/');
          }}
        >
          {t('settings.signOut')}
        </button>
      )}
    </div>
  );
}
