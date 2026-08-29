import type { RoleRow } from "./types";

export const statusTone = (value: unknown) =>
  ["active", "approved", "resolved", "success", "활성", "정상"].includes(
    String(value).toLowerCase(),
  )
    ? ("positive" as const)
    : ["pending", "review", "대기"].some((item) =>
          String(value).toLowerCase().includes(item),
        )
      ? ("warning" as const)
      : ["disabled", "rejected", "blocked", "failed", "비활성"].some((item) =>
            String(value).toLowerCase().includes(item),
          )
        ? ("danger" as const)
        : ("neutral" as const);

export function roleRows(value: unknown): RoleRow[] {
  if (Array.isArray(value)) return value as RoleRow[];
  const raw = value as { items?: RoleRow[]; roles?: RoleRow[] } | undefined;
  return raw?.items || raw?.roles || [];
}
