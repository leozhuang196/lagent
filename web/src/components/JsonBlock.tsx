import type { JsonValue } from '../api';
import { prettyJson } from '../lib/format';

export function JsonBlock({ value, maxHeight = 280 }: { value: JsonValue | undefined; maxHeight?: number }) {
  if (value === undefined || value === null) return <span className="muted">（无参数）</span>;
  return (
    <pre className="code" style={{ maxHeight }}>
      {prettyJson(value)}
    </pre>
  );
}
