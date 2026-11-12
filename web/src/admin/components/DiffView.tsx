import { Typography } from 'antd';
import type { Change } from '../../api';
import { prettyJson } from '../../lib/format';

function render(v: unknown): string {
  if (v === undefined) return '（无）';
  if (typeof v === 'string') return v;
  return prettyJson(v as never);
}

/** Field-by-field difference between two asset versions (backend diff). */
export function DiffView({ changes }: { changes: Change[] | null }) {
  if (!changes || changes.length === 0) {
    return <Typography.Text type="secondary">两个版本内容相同。</Typography.Text>;
  }
  return (
    <div>
      {changes.map((c) => (
        <div className="diff-row" key={c.path}>
          <div className="diff-path">{c.path}</div>
          <div className="diff-value">
            {c.before !== undefined && <span className="diff-before pre-wrap">{render(c.before)}</span>}
            {c.after !== undefined && <span className="diff-after pre-wrap">{render(c.after)}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}