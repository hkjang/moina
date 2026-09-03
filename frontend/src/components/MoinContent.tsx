import { Link } from "react-router-dom";

type ContentPart =
  | { type: "text"; value: string }
  | { type: "link"; value: string; target: string }
  | { type: "mention"; value: string; target: string }
  | { type: "topic"; value: string; target: string };

// The URL branch comes first so a link is one token: without it the @ and # in
// a path would be torn out as a mention or a topic. Only http and https are
// recognised, which is what makes the matched text safe to use as an href - a
// javascript: or data: URL never becomes a part of type "link". The character
// class is the RFC 3986 set, so Korean written straight after a link stops the
// URL instead of being swallowed into it.
const richTokenPattern =
  /(https?:\/\/[\w-][\w\-.~:/?#[\]@!$&'()*+,;=%]*)|(^|[^\p{L}\p{N}._-])(@([\p{L}\p{N}][\p{L}\p{N}._-]{2,39}))|(#[\p{L}\p{N}_]{1,50})/giu;

// A sentence ends "...보세요 https://moina.example." far more often than a URL
// really ends in punctuation, so the tail is given back to the text. A closing
// bracket is kept only when the URL opened one, which is how a Wikipedia style
// link survives.
function trimURLTail(url: string): string {
  let value = url;
  while (value.length > 0) {
    const last = value[value.length - 1];
    const opener = last === ")" ? "(" : last === "]" ? "[" : "";
    if (!".,;:!?'\"".includes(last) && !(opener && !value.includes(opener))) break;
    value = value.slice(0, -1);
  }
  return value;
}

export function moinContentParts(content: string): ContentPart[] {
  const parts: ContentPart[] = [];
  let cursor = 0;
  richTokenPattern.lastIndex = 0;
  for (let match = richTokenPattern.exec(content); match; match = richTokenPattern.exec(content)) {
    const url = match[1] ? trimURLTail(match[1]) : "";
    const token = url || match[3] || match[5];
    const tokenStart = match.index + (match[3] ? match[2].length : 0);
    if (tokenStart > cursor)
      parts.push({ type: "text", value: content.slice(cursor, tokenStart) });
    if (url)
      parts.push({ type: "link", value: url, target: url });
    else if (match[3])
      parts.push({ type: "mention", value: token, target: match[4] });
    else
      parts.push({ type: "topic", value: token, target: token.slice(1) });
    cursor = tokenStart + token.length;
    // A trimmed tail has to be scanned again; it may itself hold a mention.
    richTokenPattern.lastIndex = cursor;
  }
  if (cursor < content.length)
    parts.push({ type: "text", value: content.slice(cursor) });
  return parts;
}

export function MoinContent({ content, moinId }: { content: string; moinId: string }) {
  return (
    <div className="moin-content">
      {moinContentParts(content).map((part, index) =>
        part.type === "link" ? (
          <a
            className="moin-rich-link"
            href={part.target}
            target="_blank"
            rel="noopener noreferrer nofollow"
            key={`${index}-${part.value}`}
          >
            {part.value}
          </a>
        ) : part.type === "mention" ? (
          <Link className="moin-rich-link" to={`/profile/${encodeURIComponent(part.target)}`} key={`${index}-${part.value}`}>
            {part.value}
          </Link>
        ) : part.type === "topic" ? (
          <Link className="moin-rich-link" to={`/topics/${encodeURIComponent(part.target)}`} key={`${index}-${part.value}`}>
            {part.value}
          </Link>
        ) : !/[\p{L}\p{N}]/u.test(part.value) ? (
          <span key={`${index}-${part.value}`}>{part.value}</span>
        ) : (
          <Link className="moin-content-detail" to={`/moin/${encodeURIComponent(moinId)}`} key={`${index}-${part.value}`}>
            {part.value}
          </Link>
        ),
      )}
    </div>
  );
}
