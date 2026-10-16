import {Link, useNavigate} from 'react-router';
import {Icon} from '../components/Icon';
import {EmptyState} from '../components/States';
import {useAuth} from '../lib/auth';
import {themeLabels, useTheme, type ThemePref} from '../lib/theme';

export const meta = () => [{title: 'REEL · 我的'}];

// MobileLibrary (我的 tab): account, library links, appearance.
// TODO(tech plan 3.13 playlists): multiple 收藏夹.
export default function Me() {
  const {status, user, signOut} = useAuth();
  const {pref, setPref} = useTheme();
  const navigate = useNavigate();

  return (
    <div className="page me-page">
      {status === 'anon' && (
        <EmptyState
          icon="me"
          title="登录后同步你的观看记录"
          body="不用登录也能看全部视频；登录后记住看到哪里，收藏喜欢的作品。"
          actions={[{to: '/login?next=%2Fme', label: '登录 / 注册'}]}
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
          <nav className="list-links" aria-label="我的内容">
            <Link to="/history" className="list-link">
              <Icon name="history" />
              观看记录
            </Link>
            <Link to="/favorites" className="list-link">
              <Icon name="favorites" />
              收藏夹
            </Link>
            <Link to="/later" className="list-link">
              <Icon name="later" />
              稍后观看
            </Link>
            <Link to="/reservations" className="list-link">
              <Icon name="calendar" />
              我的预约
            </Link>
            <Link to="/notifications" className="list-link">
              <Icon name="bell" />
              通知
            </Link>
            <Link to="/settings" className="list-link">
              <Icon name="settings" />
              设置
            </Link>
          </nav>
        </>
      )}

      <section className="shelf">
        <h2 className="section-title">外观</h2>
        <div className="segmented" role="radiogroup" aria-label="外观">
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
          退出登录
        </button>
      )}
    </div>
  );
}
