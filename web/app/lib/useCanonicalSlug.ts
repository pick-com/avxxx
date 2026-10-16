import {useEffect} from 'react';
import {useLocation, useNavigate} from './nav';

/**
 * Merged entities: the API answers an old slug with 301 to the surviving
 * entity, and fetch() follows it, so the body carries the canonical slug. When
 * that differs from the URL, replace the URL (no new history entry) so shares,
 * favorites and watch progress use the surviving slug.
 */
export function useCanonicalSlug(kind: 'releases' | 'performers', urlSlug: string, entitySlug: string | undefined) {
  const navigate = useNavigate();
  const location = useLocation();
  useEffect(() => {
    if (!entitySlug || !urlSlug || entitySlug === urlSlug) return;
    navigate(`/${kind}/${encodeURIComponent(entitySlug)}${location.search}${location.hash}`, {replace: true, state: location.state});
  }, [entitySlug, urlSlug, kind, navigate, location.search, location.hash, location.state]);
}
