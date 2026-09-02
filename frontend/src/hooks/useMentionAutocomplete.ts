import { useEffect, useRef, useState, type RefObject } from 'react';
import { normalizeProfile } from '../api/adapters';
import { apiRequest } from '../api/client';
import type { Profile } from '../types';

// Debounce long enough that a fast typist does not fire a request per keystroke,
// short enough that the list is there by the time they stop.
const MENTION_DEBOUNCE_MS = 180;
const MENTION_LIMIT = 6;

export interface MentionSearch {
  start: number;
  end: number;
  query: string;
}

// A mention is only active when the @ starts a word, so an email address or a
// mid-word @ never opens the list.
export function activeMention(value: string, caret: number): MentionSearch | null {
  const before = value.slice(0, caret);
  const match = before.match(/(?:^|[^\p{L}\p{N}._-])@([\p{L}\p{N}._-]{0,39})$/u);
  if (!match) return null;
  const query = match[1];
  return { start: caret - query.length - 1, end: caret, query };
}

interface MentionOptions {
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  // Called with the rewritten value and where the caret should land in it.
  onReplace: (next: string, caret: number) => void;
}

export function useMentionAutocomplete({ textareaRef, onReplace }: MentionOptions) {
  const [search, setSearch] = useState<MentionSearch | null>(null);
  const [candidates, setCandidates] = useState<Profile[]>([]);
  const [loading, setLoading] = useState(false);
  const [index, setIndex] = useState(0);
  const timer = useRef<number | undefined>(undefined);
  // Every in-flight lookup carries the sequence it started at, so a slow
  // response for an abandoned prefix cannot overwrite a newer result.
  const sequence = useRef(0);
  const disposed = useRef(false);

  useEffect(() => {
    disposed.current = false;
    return () => {
      disposed.current = true;
      window.clearTimeout(timer.current);
    };
  }, []);

  const close = () => {
    window.clearTimeout(timer.current);
    sequence.current += 1;
    setSearch(null);
    setCandidates([]);
    setLoading(false);
    setIndex(0);
  };

  const find = (value: string, caret: number) => {
    const active = activeMention(value, caret);
    window.clearTimeout(timer.current);
    if (!active) {
      close();
      return;
    }
    const current = ++sequence.current;
    setSearch(active);
    setCandidates([]);
    setLoading(true);
    setIndex(0);
    timer.current = window.setTimeout(() => {
      const path = active.query
        ? `/search?q=${encodeURIComponent(active.query)}&type=users&limit=${MENTION_LIMIT}`
        : `/search?recommended=true&type=users&limit=${MENTION_LIMIT}`;
      void apiRequest<unknown>(path)
        .then((result) => {
          if (disposed.current || current !== sequence.current) return;
          const users =
            result && typeof result === 'object' && Array.isArray((result as { users?: unknown[] }).users)
              ? (result as { users: unknown[] }).users
              : [];
          setCandidates(users.map(normalizeProfile));
        })
        .catch(() => {
          if (!disposed.current && current === sequence.current) setCandidates([]);
        })
        .finally(() => {
          if (!disposed.current && current === sequence.current) setLoading(false);
        });
    }, MENTION_DEBOUNCE_MS);
  };

  const insert = (candidate: Profile, value: string) => {
    if (!search) return;
    const next = `${value.slice(0, search.start)}@${candidate.username} ${value.slice(search.end)}`;
    const caret = search.start + candidate.username.length + 2;
    onReplace(next, caret);
    close();
    window.requestAnimationFrame(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(caret, caret);
    });
  };

  // step moves the highlight and wraps at both ends so the list is fully
  // reachable with the arrow keys alone.
  const step = (direction: 1 | -1) => {
    setIndex((value) =>
      candidates.length === 0
        ? 0
        : (value + direction + candidates.length) % candidates.length,
    );
  };

  return { search, candidates, loading, index, setIndex, find, close, insert, step };
}
