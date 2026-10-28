import { formatCount } from "../../lib/format";

interface TokenCounts {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens?: number;
  cache_creation_tokens?: number;
}

/** Input/output tokens, with the cached share of input on a second line. */
export default function TokensCell({ row }: { row: TokenCounts }) {
  const cacheRead = row.cache_read_tokens ?? 0;
  const cacheWrite = row.cache_creation_tokens ?? 0;
  return (
    <span className="text-base-content/70">
      {formatCount(row.input_tokens)}/{formatCount(row.output_tokens)}
      {cacheRead + cacheWrite > 0 ? (
        <span
          className="block text-xs text-base-content/50"
          title="Cache read / cache write tokens, included in input"
        >
          {formatCount(cacheRead)}/{formatCount(cacheWrite)} cached
        </span>
      ) : null}
    </span>
  );
}
