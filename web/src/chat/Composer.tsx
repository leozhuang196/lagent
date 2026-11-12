import { useRef, useState, type KeyboardEvent } from 'react';
import { Button, Input, Popconfirm, Space, Tooltip } from 'antd';
import { SendOutlined, StopOutlined } from '@ant-design/icons';
import type { TextAreaRef } from 'antd/es/input/TextArea';

interface Props {
  busy: boolean;
  running: boolean;
  disabled?: boolean;
  onSend(text: string): Promise<boolean>;
  onCancel(): Promise<void>;
}

/** Enter sends, Shift+Enter inserts a newline; IME composition is respected. */
export function Composer({ busy, running, disabled, onSend, onCancel }: Props) {
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const ref = useRef<TextAreaRef>(null);

  const send = async () => {
    const t = text.trim();
    if (!t || sending || disabled) return;
    setSending(true);
    try {
      if (await onSend(t)) setText('');
    } finally {
      setSending(false);
      ref.current?.focus();
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void send();
    }
  };

  return (
    <div className="composer">
      <Input.TextArea
        ref={ref}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKeyDown}
        autoSize={{ minRows: 2, maxRows: 10 }}
        placeholder={running ? '助手正在处理，新消息会并入当前轮次…（Enter 发送，Shift+Enter 换行）' : '输入消息（Enter 发送，Shift+Enter 换行）'}
        disabled={disabled}
        aria-label="消息输入框"
      />
      <Space className="composer-actions">
        {busy && (
          <Popconfirm
            title="停止当前轮次？"
            description="已停止，已发生的操作不会撤回"
            okText="停止"
            cancelText="继续运行"
            onConfirm={async () => {
              setStopping(true);
              try {
                await onCancel();
              } finally {
                setStopping(false);
              }
            }}
          >
            <Button danger icon={<StopOutlined />} loading={stopping}>
              停止
            </Button>
          </Popconfirm>
        )}
        <Tooltip title="Enter 发送">
          <Button type="primary" icon={<SendOutlined />} onClick={() => void send()} loading={sending} disabled={disabled || !text.trim()}>
            发送
          </Button>
        </Tooltip>
      </Space>
    </div>
  );
}
