import { useInfiniteQuery } from '@tanstack/react-query';
import { sessionsApi, type Session } from '../api';

/** Cursor-paginated session list (?after=&limit=). */
export function useSessionList(all = false, limit = 30) {
  const q = useInfiniteQuery({
    queryKey: ['sessions', { all, limit }],
    queryFn: ({ pageParam }) => sessionsApi.list({ after: pageParam || undefined, limit, all }),
    initialPageParam: '',
    getNextPageParam: (last) => last.next || undefined,
  });
  const items: Session[] = q.data?.pages.flatMap((p) => p.items ?? []) ?? [];
  return { ...q, items };
}
