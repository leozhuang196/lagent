import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { Page } from '../api';

/**
 * A cursor-paginated list: the first page comes from the query cache and
 * "load more" appends further pages. When the filter key changes the appended
 * pages are dropped (they belong to the old filter).
 */
export function usePagedList<T>(queryKey: unknown[], fetch: (after?: string) => Promise<Page<T>>) {
  const q = useQuery({ queryKey, queryFn: () => fetch() });
  const keyStr = JSON.stringify(queryKey);
  const [state, setState] = useState<{ key: string; pages: Page<T>[] }>({ key: keyStr, pages: [] });
  const pages = state.key === keyStr ? state.pages : [];
  const [loadingMore, setLoadingMore] = useState(false);

  const first = q.data;
  const items: T[] = [...(first?.items ?? []), ...pages.flatMap((p) => p.items ?? [])];
  const next = pages.length > 0 ? pages[pages.length - 1].next : first?.next;

  const loadMore = async () => {
    if (!next || loadingMore) return;
    setLoadingMore(true);
    try {
      const p = await fetch(next);
      setState((s) => ({ key: keyStr, pages: [...(s.key === keyStr ? s.pages : []), p] }));
    } finally {
      setLoadingMore(false);
    }
  };

  return { ...q, items, next, loadMore, loadingMore };
}