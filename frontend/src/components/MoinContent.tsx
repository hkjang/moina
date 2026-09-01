import { Link } from "react-router-dom";

type ContentPart =
  | { type: "text"; value: string }
  | { type: "mention"; value: string; target: string }
  | { type: "topic"; value: string; target: string };

const richTokenPattern = /(^|[^\p{L}\p{N}._-])(@([\p{L}\p{N}][\p{L}\p{N}._-]{2,39}))|(#[\p{L}\p{N}_]{1,50})/giu;

export function moinContentParts(content: string): ContentPart[] {
  const parts: ContentPart[] = [];
  let cursor = 0;
  richTokenPattern.lastIndex = 0;
  for (let match = richTokenPattern.exec(content); match; match = richTokenPattern.exec(content)) {
    const token = match[2] || match[4];
    const tokenStart = match.index + (match[2] ? match[1].length : 0);
    if (tokenStart > cursor)
      parts.push({ type: "text", value: content.slice(cursor, tokenStart) });
    if (match[2])
      parts.push({ type: "mention", value: token, target: match[3] });
    else
      parts.push({ type: "topic", value: token, target: token.slice(1) });
    cursor = tokenStart + token.length;
  }
  if (cursor < content.length)
    parts.push({ type: "text", value: content.slice(cursor) });
  return parts;
}

export function MoinContent({ content, moinId }: { content: string; moinId: string }) {
  return (
    <div className="moin-content">
      {moinContentParts(content).map((part, index) =>
        part.type === "mention" ? (
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
