import type { CursorPage, Moin } from '../types';

export type FeedCursor = string | null;
export type FeedPageMap = Map<FeedCursor, CursorPage<Moin>>;

export function replaceFeedPage(pages: FeedPageMap, cursor: FeedCursor, page: CursorPage<Moin>) {
  if (cursor === null) return new Map<FeedCursor, CursorPage<Moin>>([[null, page]]);
  const next = new Map(pages);
  next.set(cursor, page);
  return next;
}

export function feedCursorChain(pages: FeedPageMap) {
  const cursors: FeedCursor[] = [];
  const visited = new Set<FeedCursor>();
  let cursor: FeedCursor = null;
  while (!visited.has(cursor)) {
    const page = pages.get(cursor);
    if (!page) break;
    cursors.push(cursor);
    visited.add(cursor);
    if (!page.nextCursor) break;
    cursor = page.nextCursor;
  }
  return cursors;
}

export function mergeFeedPages(pages: FeedPageMap) {
  const order: string[] = [];
  const byID = new Map<string, Moin>();
  for (const cursor of feedCursorChain(pages)) {
    for (const item of pages.get(cursor)?.items || []) {
      if (!item.id) continue;
      if (!byID.has(item.id)) order.push(item.id);
      byID.set(item.id, item);
    }
  }
  return order.map((id) => byID.get(id)!).filter(Boolean);
}

export function updateMoinInFeed(pages: FeedPageMap, changed: Moin) {
  let matched = false;
  const next = new Map<FeedCursor, CursorPage<Moin>>();
  for (const [cursor, page] of pages) {
    let pageMatched = false;
    const items = page.items.map((item) => {
      if (item.id !== changed.id) return item;
      matched = true;
      pageMatched = true;
      return changed;
    });
    next.set(cursor, pageMatched ? { ...page, items } : page);
  }
  return matched ? next : pages;
}

export function nextUnseenCursor(pages: FeedPageMap, cursor: FeedCursor) {
  const candidate = pages.get(cursor)?.nextCursor;
  return candidate && !feedCursorChain(pages).includes(candidate) ? candidate : undefined;
}
