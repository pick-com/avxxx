import {useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent} from 'react';
import {useNavigate} from '../lib/nav';
import {catalogSearch, isAbort} from '../lib/api';
import {formatDate} from '../lib/format';
import {useT} from '../lib/i18n';
import {getSuggestions, suggestionPath, type Suggestion} from '../lib/search';
import {Icon} from './Icon';

const SUGGEST_DELAY_MS = 200;
const SUGGEST_LIMIT = 8;

// Older backends have no /api/search/suggest: remember that for the page
// lifetime and fall back to the first catalog-search hits.
let suggestMissing = false;

async function loadSuggestions(q: string, signal: AbortSignal): Promise<Suggestion[]> {
  if (!suggestMissing) {
    const items = await getSuggestions(q, SUGGEST_LIMIT, signal);
    if (items) return items;
    suggestMissing = true;
  }
  const hits = await catalogSearch(q, SUGGEST_LIMIT, signal);
  return hits.map(h => ({type: h.type, name: h.name, id: h.id, slug: h.slug, release_date: h.release_date}));
}

const KIND_KEY = {
  release: 'suggest.release',
  performer: 'suggest.performer',
  upcoming: 'suggest.upcoming',
  tag: 'suggest.tag',
  query: 'suggest.query',
} as const;

/**
 * Search input with typed suggestions (Browse showSuggest / MobileSearchTyping):
 * 视频 / 演员 / 即将上线 / 标签 / 热搜 from GET /api/search/suggest, debounced,
 * the previous request aborted on every keystroke. ↑ ↓ move, Enter opens the
 * highlighted item (or searches the text), Esc closes. Submitting goes to
 * /search?q=.
 */
export function SearchBox({initialQuery = '', autoFocus = false, className = ''}: {initialQuery?: string; autoFocus?: boolean; className?: string}) {
  const t = useT();
  const navigate = useNavigate();
  const listId = useId();
  const [value, setValue] = useState(initialQuery);
  const [items, setItems] = useState<Suggestion[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const typed = useRef(false);

  useEffect(() => {
    setValue(initialQuery);
    typed.current = false;
  }, [initialQuery]);

  useEffect(() => {
    const q = value.trim();
    if (!typed.current || !q) {
      setItems([]);
      setActive(-1);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      loadSuggestions(q, controller.signal).then(
        result => {
          if (controller.signal.aborted) return;
          setItems(result);
          setActive(-1);
        },
        error => {
          if (!isAbort(error) && !controller.signal.aborted) setItems([]);
        },
      );
    }, SUGGEST_DELAY_MS);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [value]);

  const go = (s: Suggestion) => {
    setOpen(false);
    setActive(-1);
    if (s.type === 'query' || s.type === 'tag') setValue(s.name);
    navigate(suggestionPath(s));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (open && active >= 0 && items[active]) {
      go(items[active]);
      return;
    }
    const q = value.trim();
    if (!q) return;
    setOpen(false);
    navigate(`/search?q=${encodeURIComponent(q)}`);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Escape') {
      setOpen(false);
      setActive(-1);
      return;
    }
    if (!items.length) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setOpen(true);
      setActive(i => (i + 1 >= items.length ? -1 : i + 1));
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setOpen(true);
      setActive(i => (i < 0 ? items.length - 1 : i - 1));
    }
  };

  const expanded = open && items.length > 0;

  const sub = (s: Suggestion) => {
    if (s.type === 'upcoming' && s.scheduled_date) return t('suggest.upcomingOn', {date: formatDate(s.scheduled_date)});
    if (s.type === 'release' && s.release_date) return formatDate(s.release_date);
    return '';
  };

  return (
    <form role="search" className={`searchbox ${className}`} onSubmit={submit}>
      <label className="searchbox-field">
        <Icon name="search" size={16} />
        <input
          type="search"
          name="q"
          value={value}
          autoFocus={autoFocus}
          autoComplete="off"
          enterKeyHint="search"
          maxLength={200}
          placeholder={t('discover.searchHint')}
          aria-label={t('discover.searchHint')}
          role="combobox"
          aria-expanded={expanded}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={expanded && active >= 0 ? `${listId}-${active}` : undefined}
          onChange={event => {
            typed.current = true;
            setValue(event.target.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={onKeyDown}
        />
      </label>
      <ul id={listId} role="listbox" className="suggest" hidden={!expanded} aria-label={t('search.suggest')}>
        {items.map((s, i) => {
          const kind = KIND_KEY[s.type as keyof typeof KIND_KEY];
          const detail = sub(s);
          return (
            <li
              key={`${s.type}-${s.id ?? s.slug ?? s.name}`}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              className={`suggest-item suggest-${s.type}${i === active ? ' is-active' : ''}${i > 0 && items[i - 1].type !== s.type ? ' suggest-group-start' : ''}`}
              onMouseDown={event => event.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => go(s)}
            >
              {kind && <span className="suggest-kind">{t(kind)}</span>}
              <span className="suggest-text">{s.type === 'tag' ? `#${s.name}` : s.name}</span>
              {detail && <span className="suggest-sub">{detail}</span>}
            </li>
          );
        })}
      </ul>
    </form>
  );
}
