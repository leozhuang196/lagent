import type { ReactNode } from 'react';
import { Button, Empty, Result, Spin } from 'antd';

export function Loading({ tip = '加载中…' }: { tip?: string }) {
  return (
    <div className="state-center">
      <Spin tip={tip}>
        <div style={{ width: 120, height: 60 }} />
      </Spin>
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: string; onRetry?: () => void }) {
  return (
    <Result
      status="error"
      title="加载失败"
      subTitle={error}
      extra={onRetry ? <Button onClick={onRetry}>重试</Button> : undefined}
    />
  );
}

export function EmptyState({ text, extra }: { text: string; extra?: ReactNode }) {
  return (
    <div className="state-center">
      <Empty description={text}>{extra}</Empty>
    </div>
  );
}
