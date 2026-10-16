import {createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode} from 'react';
import {AuthForm, type AuthMode} from './AuthForm';
import {Sheet} from './Sheet';
import {useAuth} from '../lib/auth';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {useLocation} from '../lib/nav';
import {clearPending, savePending, setPendingMethod, takePending, type PendingPayload} from '../lib/pendingAction';

// Guest sign-in prompt (CTA). A guest action that needs an account (收藏,
// 稍后观看, 关注, 预约, 弹幕, 评论, 短评, 点赞, 举报) opens this sheet instead of
// leaving for /login: a centred modal on desktop, a bottom sheet on phones
// (Sheet + .auth-sheet). It holds the shared AuthForm (Google when
// configured, 登录 / 注册 with the 18+ and terms checks; 忘记密码 links to
// /login?mode=forgot) and 先逛逛, which closes it. Nothing navigates and the
// player keeps playing underneath.
//
// The action itself waits in lib/pendingAction (sessionStorage, 10 minutes)
// and the component that owns it finishes it once signed in, through
// usePendingAction: in place after a password sign-in, or when the page is
// back after the Google redirect. It runs once and says so in a toast
// (「已登录，并已收藏」); drafts go back into their input, never posted.

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

const TOAST_MS = 4000;

/** `target` (and `payload`) keep the action to finish after the sign-in; without it nothing is resumed. */
export type ResumeSpec = {target: string; payload?: PendingPayload};

type PromptValue = {
  /**
   * True when signed in (go ahead). Otherwise opens the sign-in sheet for
   * `action` and returns false; while the session is still loading it does
   * nothing and returns false.
   */
  requireAuth: (action: AuthAction, resume?: ResumeSpec) => boolean;
  /** Shows a short confirmation (role=status). */
  notify: (message: string) => void;
};

const PromptContext = createContext<PromptValue>({requireAuth: () => false, notify: () => {}});

export const useRequireAuth = () => useContext(PromptContext).requireAuth;

/**
 * Finishes the stored action `kind` for `target` once signed in and `ready`
 * (the data it depends on has loaded for the account). The handler runs once;
 * a string it returns is shown as the toast.
 */
export function usePendingAction(
  kind: AuthAction,
  target: string,
  handler: (payload: PendingPayload) => string | null | void | Promise<string | null | void>,
  ready = true,
) {
  const {status} = useAuth();
  const {notify} = useContext(PromptContext);
  const run = useRef(handler);
  run.current = handler;
  useEffect(() => {
    if (status !== 'user' || !ready || !target) return;
    const pending = takePending(kind, target);
    if (!pending) return;
    void Promise.resolve(run.current(pending.payload ?? {})).then(
      message => message && notify(message),
      () => {},
    );
  }, [status, ready, kind, target, notify]);
}

export function AuthPromptProvider({children}: {children: ReactNode}) {
  const {status} = useAuth();
  const [action, setAction] = useState<AuthAction | null>(null);
  const [toast, setToast] = useState<{text: string; id: number} | null>(null);

  const requireAuth = useCallback(
    (next: AuthAction, resume?: ResumeSpec) => {
      if (status === 'user') return true;
      if (status === 'anon') {
        if (resume?.target) savePending({kind: next, target: resume.target, payload: resume.payload});
        else clearPending();
        setAction(next);
      }
      return false;
    },
    [status],
  );

  const notify = useCallback((text: string) => setToast({text, id: Date.now()}), []);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), TOAST_MS);
    return () => window.clearTimeout(timer);
  }, [toast]);

  const value = useMemo(() => ({requireAuth, notify}), [requireAuth, notify]);
  return (
    <PromptContext.Provider value={value}>
      {children}
      {action && (
        <AuthSheet
          action={action}
          onClose={signedIn => {
            // Dismissed without signing in: nothing is left waiting.
            if (!signedIn) clearPending();
            setAction(null);
          }}
        />
      )}
      <div className="auth-toast-region" role="status" aria-live="polite">
        {toast && (
          <div key={toast.id} className="auth-toast">
            {toast.text}
          </div>
        )}
      </div>
    </PromptContext.Provider>
  );
}

function AuthSheet({action, onClose}: {action: AuthAction; onClose: (signedIn: boolean) => void}) {
  const t = useT();
  const {status, signIn} = useAuth();
  const location = useLocation();
  const [mode, setMode] = useState<AuthMode>('login');
  const title = t(TITLES[action]);
  const here = location.pathname + location.search;
  const openedAt = useRef(location.pathname);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  // Leaving the page (忘记密码, the legal links) closes the sheet.
  useEffect(() => {
    if (location.pathname !== openedAt.current) closeRef.current(false);
  }, [location.pathname]);

  return (
    <Sheet
      open
      onClose={() => onClose(status === 'user')}
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
          onOAuthStart={provider => setPendingMethod(provider)}
          onSignedIn={(result, remember, method) => {
            setPendingMethod(method);
            signIn(result, remember);
            // A sign-in that cancelled a pending deletion keeps the sheet for its notice and 继续.
            if (!result.deletion_cancelled) onClose(true);
          }}
          onContinue={() => onClose(true)}
          footer={
            <button type="button" className="auth-skip text-btn" onClick={() => onClose(status === 'user')}>
              {t('authPrompt.skip')}
            </button>
          }
        />
      </div>
    </Sheet>
  );
}
