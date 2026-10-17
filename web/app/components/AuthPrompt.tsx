import {createContext, useCallback, useContext, useMemo, useState, type ReactNode} from 'react';
import {AuthForm, type AuthMode} from './AuthForm';
import {Sheet} from './Sheet';
import {useAuth} from '../lib/auth';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {useLocation} from '../lib/nav';

// Guest sign-in prompt (CTA). A guest action that needs an account (收藏,
// 稍后观看, 关注, 预约, 弹幕, 评论, 短评, 点赞, 举报) opens this sheet instead of
// leaving for /login: a centred modal on desktop, a bottom sheet on phones
// (Sheet + .auth-sheet). It holds the shared AuthForm (Google when
// configured, 登录 / 注册 tabs with the 18+ and terms checks; 忘记密码 links
// to /login?mode=forgot) and 先逛逛, which closes it. Nothing navigates and
// the player keeps playing underneath.

export type AuthAction = 'favorite' | 'later' | 'follow' | 'reserve' | 'danmaku' | 'comment' | 'review' | 'like' | 'report' | 'other';

const TITLES: Record<AuthAction, MessageKey> = {
  favorite: 'authPrompt.titleFavorite',
  later: 'authPrompt.titleLater',
  follow: 'authPrompt.titleFollow',
  reserve: 'authPrompt.titleReserve',
  danmaku: 'authPrompt.titleDanmaku',
  comment: 'authPrompt.titleComment',
  review: 'authPrompt.titleReview',
  like: 'authPrompt.titleLike',
  report: 'authPrompt.titleReport',
  other: 'authPrompt.titleOther',
};

type PromptValue = {
  /**
   * True when signed in (go ahead). Otherwise opens the sign-in sheet for
   * `action` and returns false; while the session is still loading it does
   * nothing and returns false.
   */
  requireAuth: (action: AuthAction) => boolean;
};

const PromptContext = createContext<PromptValue>({requireAuth: () => false});

export const useRequireAuth = () => useContext(PromptContext).requireAuth;

export function AuthPromptProvider({children}: {children: ReactNode}) {
  const {status} = useAuth();
  const [action, setAction] = useState<AuthAction | null>(null);

  const requireAuth = useCallback(
    (next: AuthAction) => {
      if (status === 'user') return true;
      if (status === 'anon') setAction(next);
      return false;
    },
    [status],
  );

  const value = useMemo(() => ({requireAuth}), [requireAuth]);
  return (
    <PromptContext.Provider value={value}>
      {children}
      {action && <AuthSheet action={action} onClose={() => setAction(null)} />}
    </PromptContext.Provider>
  );
}

function AuthSheet({action, onClose}: {action: AuthAction; onClose: () => void}) {
  const t = useT();
  const {signIn} = useAuth();
  const location = useLocation();
  const [mode, setMode] = useState<AuthMode>('login');
  const title = t(TITLES[action]);
  const here = location.pathname + location.search;

  return (
    <Sheet
      open
      onClose={onClose}
      label={title}
      className="auth-sheet"
      header={
        <div className="auth-sheet-head">
          <h2 className="sheet-title">{title}</h2>
          <p className="auth-sub">{t('authPrompt.sub')}</p>
        </div>
      }
    >
      <div className="auth-sheet-body">
        <AuthForm
          mode={mode}
          onMode={m => setMode(m === 'forgot' ? 'login' : m)}
          next={here}
          idPrefix="sheet"
          forgotHref="/login?mode=forgot"
          ssoFirst
          onSignedIn={(result, remember) => {
            signIn(result, remember);
            // A sign-in that cancelled a pending deletion keeps the sheet for its notice and 继续.
            if (!result.deletion_cancelled) onClose();
          }}
          onContinue={onClose}
          footer={
            <button type="button" className="auth-skip text-btn" onClick={onClose}>
              {t('authPrompt.skip')}
            </button>
          }
        />
      </div>
    </Sheet>
  );
}
