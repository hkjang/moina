import type { Moin, SignalType } from '../types';

const count = (value: number | undefined, delta: number) => Math.max(0, (value || 0) + delta);

export function toggleMoinSignal(moin: Moin, type: SignalType): Moin {
  const current = moin.viewer?.signals || [];
  const active = current.includes(type);
  const signals = active ? current.filter((item) => item !== type) : [...current, type];
  return {
    ...moin,
    viewer: { ...moin.viewer, signals },
    counts: {
      ...moin.counts,
      signals: { ...moin.counts?.signals, [type]: count(moin.counts?.signals?.[type], active ? -1 : 1) },
    },
  };
}

export function toggleMoinBookmark(moin: Moin): Moin {
  const active = moin.viewer?.bookmarked === true;
  return {
    ...moin,
    viewer: { ...moin.viewer, bookmarked: !active },
    counts: { ...moin.counts, bookmarks: count(moin.counts?.bookmarks, active ? -1 : 1) },
  };
}

export function toggleMoinRemoin(moin: Moin): Moin {
  const active = moin.viewer?.remoined === true;
  return {
    ...moin,
    viewer: { ...moin.viewer, remoined: !active },
    counts: { ...moin.counts, remoins: count(moin.counts?.remoins, active ? -1 : 1) },
  };
}
