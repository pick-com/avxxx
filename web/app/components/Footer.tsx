import {Link} from 'react-router';
import {useConsent} from '../lib/consent';

// LegalPage footer. Only destinations that exist are linked: 帮助中心 / 反馈 /
// 合作 pages have no backend yet.
// TODO(tech plan 3.13 feedback): 帮助中心、反馈问题、内容合作 links.
const LEGAL_LINKS: [string, string][] = [
  ['/legal/privacy', '隐私政策'],
  ['/legal/terms', '用户协议'],
  ['/legal/cookies', 'Cookie 政策'],
  ['/legal/copyright', '版权政策'],
  ['/legal/community', '社区规则'],
  ['/legal/ads', '广告说明'],
  ['/legal/performer-info', '演员信息更正'],
];

export function Footer() {
  const {openPrefs} = useConsent();
  return (
    <footer className="site-footer">
      <nav className="footer-links" aria-label="法律与政策">
        {LEGAL_LINKS.map(([to, label]) => (
          <Link key={to} to={to} className="footer-link">
            {label}
          </Link>
        ))}
        <button type="button" className="footer-link" onClick={openPrefs}>
          Cookie 设置
        </button>
        <button type="button" className="footer-link" onClick={openPrefs}>
          不出售或分享我的个人信息
        </button>
      </nav>
      <p className="footer-note">© 2026 [公司名称] · [备案号 / 公司注册信息]</p>
    </footer>
  );
}
