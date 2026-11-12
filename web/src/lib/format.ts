import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';
import 'dayjs/locale/zh-cn';
import type { JsonValue } from '../api';

dayjs.extend(relativeTime);
dayjs.locale('zh-cn');

export function fmtTime(s?: string | null): string {
  if (!s) return '—';
  const d = dayjs(s);
  if (!d.isValid() || d.year() < 2000) return '—';
  return d.format('YYYY-MM-DD HH:mm:ss');
}

export function fromNow(s?: string | null): string {
  if (!s) return '';
  const d = dayjs(s);
  return d.isValid() && d.year() >= 2000 ? d.fromNow() : '';
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export function prettyJson(v: JsonValue | undefined): string {
  if (v === undefined) return '';
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2);
    } catch {
      return v;
    }
  }
  return JSON.stringify(v, null, 2);
}

export interface ToolData {
  source?: string;
  body: string;
}

/**
 * Tool output reaches the model wrapped as <tool_data source="...">…</tool_data>
 * (prompt-injection fence, capability/tool). People see only the inner content.
 */
export function unwrapToolData(text: string): ToolData[] {
  const re = /<tool_data(?:\s+source="([^"]*)")?>\n?([\s\S]*?)\n?<\/tool_data>/g;
  const out: ToolData[] = [];
  let m: RegExpExecArray | null;
  let last = 0;
  while ((m = re.exec(text))) {
    const before = text.slice(last, m.index).trim();
    if (before) out.push({ body: before });
    out.push({ source: m[1], body: m[2].replace(/<\/tool_data_>/g, '</tool_data>') });
    last = re.lastIndex;
  }
  const rest = text.slice(last).trim();
  if (rest || out.length === 0) out.push({ body: rest });
  return out;
}

export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
