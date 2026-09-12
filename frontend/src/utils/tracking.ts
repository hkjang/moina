// Helpers for the administrator's visitor tracking form.

// The server refuses a pasted snippet above this size; the form says so before
// the round trip.
export const MAX_SNIPPET_BYTES = 8 * 1024;

export const snippetBytes = (snippet: string) => new TextEncoder().encode(snippet).length;

// The textarea holds one origin per line; commas and blanks are tolerated so a
// pasted list from a policy error still works.
export const parseAllowedHosts = (text: string) =>
  text.split(/[\n,]/).map((host) => host.trim()).filter(Boolean);
