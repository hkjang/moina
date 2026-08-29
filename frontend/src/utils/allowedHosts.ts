export function parseAllowedHosts(value: string) {
  return [...new Set(value.split(/[\n,]/).map((item) => item.trim().toLowerCase()).filter(Boolean))];
}

export function formatAllowedHosts(value: string[] | undefined) {
  return (value || []).join('\n');
}

export function endpointAuthority(value: string | undefined) {
  if (!value?.trim()) return undefined;
  try { return new URL(value).host.toLowerCase(); }
  catch { return null; }
}

export function ensureEndpointHost(hosts: string[], endpoint: string | undefined) {
  const authority = endpointAuthority(endpoint);
  if (authority === null) return { hosts, invalid: true, added: undefined };
  if (!authority || hosts.includes(authority)) return { hosts, invalid: false, added: undefined };
  return { hosts: [...hosts, authority], invalid: false, added: authority };
}
