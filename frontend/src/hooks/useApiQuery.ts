import { useCallback, useEffect, useRef, useState } from 'react';
import { apiRequest, readableError } from '../api/client';

export function useApiQuery<T>(path: string | null) {
  const [data, setData] = useState<T | undefined>();
  const [loading, setLoading] = useState(Boolean(path));
  const [error, setError] = useState<string | null>(null);
  const requestID = useRef(0);
  const load = useCallback(async () => {
    if (!path) { setLoading(false); return; }
    const current = ++requestID.current;
    setLoading(true); setError(null);
    try { const result = await apiRequest<T>(path); if (current === requestID.current) setData(result); }
    catch (cause) { if (current === requestID.current) setError(readableError(cause)); }
    finally { if (current === requestID.current) setLoading(false); }
  }, [path]);
  useEffect(() => { void load(); return () => { requestID.current += 1; }; }, [load]);
  return { data, loading, error, reload: load, setData };
}
