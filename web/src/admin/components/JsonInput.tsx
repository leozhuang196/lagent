import { useState } from 'react';
import { Input } from 'antd';
import type { JsonValue } from '../../api';

interface Props {
  // Both are optional so the component can sit inside <Form.Item name="…">,
  // which injects them at runtime (TypeScript cannot see that injection).
  value?: JsonValue | undefined;
  onChange?: (v: JsonValue | undefined) => void;
  rows?: number;
  placeholder?: string;
  ariaLabel?: string;
}

/**
 * A JSON textarea that only reports valid JSON. Text is local so typing is
 * never fought by re-serialisation; give the component a new key when the
 * source value is replaced (e.g. switching versions).
 */
export function JsonInput({ value, onChange, rows = 8, placeholder, ariaLabel }: Props) {
  const [text, setText] = useState(() => (value === undefined || value === null ? '' : JSON.stringify(value, null, 2)));
  const [err, setErr] = useState<string | null>(null);

  return (
    <div>
      <Input.TextArea
        className="mono-input"
        value={text}
        rows={rows}
        placeholder={placeholder}
        aria-label={ariaLabel}
        status={err ? 'error' : undefined}
        onChange={(e) => {
          const t = e.target.value;
          setText(t);
          if (!t.trim()) {
            setErr(null);
            onChange?.(undefined);
            return;
          }
          try {
            onChange?.(JSON.parse(t) as JsonValue);
            setErr(null);
          } catch (e2) {
            setErr(e2 instanceof Error ? e2.message : String(e2));
          }
        }}
      />
      {err && <div className="small" style={{ color: '#cf1322', marginTop: 4 }}>JSON 无效：{err}</div>}
    </div>
  );
}