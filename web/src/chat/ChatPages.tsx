import { useParams } from 'react-router-dom';
import { Typography } from 'antd';
import { ConversationView } from './ConversationView';

export function ChatHome() {
  return (
    <div className="state-center" style={{ height: '100%' }}>
      <div style={{ textAlign: 'center' }}>
        <Typography.Title level={3}>lagent</Typography.Title>
        <Typography.Text type="secondary">从左侧选择会话，或点击「新会话」开始。</Typography.Text>
      </div>
    </div>
  );
}

export function ChatSession() {
  const { sessionId } = useParams();
  if (!sessionId) return null;
  // key: a fresh hook state per session.
  return <ConversationView key={sessionId} sessionId={sessionId} />;
}
