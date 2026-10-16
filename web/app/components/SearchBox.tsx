import {useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent} from 'react';
import {useNavigate} from 'react-router';
import {catalogSearch, isAbort, type SearchHit} from '../lib/api';
import {Icon} from './Icon';

const SUGGEST_DELAY_MS = 250;

/**
 * Search input with type-ahead (Browse showSuggest / MobileSearchTyping).
 * Suggestions come from GET /api/catalog/search; submitting goes to /search?q=.
 */
export function SearchBox({initialQuery = '', autoFocus = false, className = ''}: {initialQuery?: string; autoFocus?: boolean; className?: string}) {
  const navigate = useNavigate();
  const listId = useId();
  const [value, setValue] = useState(initialQuery);
  const [hits, setHits] = useState<SearchHit[]>([]);
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
      setHits([]);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      catalogSearch(q, 8, controller.signal).then(
        result => {
          setHits(result);
          setActive(-1);
        },
        error => {
          if (!isAbort(error)) setHits([]);
        },
      );
    }, SUGGEST_DELAY_MS);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [value]);

  const go = (hit: SearchHit) => {
    setOpen(false);
    navigate(hit.type === 'performer' ? `/performers/${encodeURIComponent(hit.slug)}` : `/releases/${encodeURIComponent(hit.slug)}`);
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (active >= 0 && hits[active]) {
      go(hits[active]);
      return;
    }
    const q = value.trim();
    if (!q) return;
    setOpen(false);
    navigate(`/search?q=${encodeURIComponent(q)}`);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (!hits.length) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setOpen(true);
      setActive(i => (i + 1) % hits.length);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setActive(i => (i <= 0 ? hits.length - 1 : i - 1));
    } else if (event.key === 'Escape') {
      setOpen(false);
      setActive(-1);
    }
  };

  const expanded = open && hits.length > 0;

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
          placeholder="搜索视频、演员"
          aria-label="搜索视频、演员"
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
      <ul id={listId} role="listbox" className="suggest" hidden={!expanded} aria-label="搜索建议">
        {hits.map((hit, i) => (
          <li
            key={`${hit.type}-${hit.id}`}
            id={`${listId}-${i}`}
            role="option"
            aria-selected={i === active}
            className={i === active ? 'suggest-item is-active' : 'suggest-item'}
            onMouseDown={event => event.preventDefault()}
            onClick={() => go(hit)}
          >
            <span className="suggest-kind">{hit.type === 'performer' ? '演员' : '视频'}</span>
            <span className="suggest-text">{hit.name}</span>
          </li>
        ))}
      </ul>
    </form>
  );
}
