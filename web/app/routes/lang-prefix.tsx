import {useEffect} from 'react';
import {useLocation, useNavigate, useParams} from 'react-router';
import {isLang, normalizeLang, useI18n} from '../lib/i18n';
import NotFound from './not-found';

// PRD 8 language-prefixed URLs (/en/releases/123, /zh-TW/...): accepted as
// entry links. The prefix picks the UI language (signed-in: PATCH ui_lang)
// and is then dropped, because the SPA's own links are unprefixed. Keeping
// the prefix everywhere plus hreflang alternates waits for SSR (see
// lib/i18n.tsx). Anything that is not a launch language is a 404.
export default function LangPrefix() {
  const {lang = ''} = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const {uiLang, setUiLang} = useI18n();
  const code = isLang(lang) ? lang : lang.length <= 7 ? normalizeLang(lang) : null;

  useEffect(() => {
    if (!code) return;
    if (code !== uiLang) setUiLang(code);
    const rest = location.pathname.slice(lang.length + 1) || '/';
    navigate(`${rest.startsWith('/') ? rest : `/${rest}`}${location.search}${location.hash}`, {replace: true, state: location.state});
    // Runs once per entry URL.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code, location.pathname]);

  return code ? null : <NotFound />;
}
