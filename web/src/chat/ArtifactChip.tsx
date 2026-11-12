import { useState } from 'react';
import { Button, Tooltip, message } from 'antd';
import { DownloadOutlined, FileOutlined } from '@ant-design/icons';
import { artifactsApi, errorText, type Artifact } from '../api';
import { fmtBytes } from '../lib/format';

/** Fetches a short-lived signed URL and opens it; links are never stored. */
export async function downloadArtifact(id: string): Promise<void> {
  const u = await artifactsApi.url(id);
  const a = document.createElement('a');
  a.href = u.url;
  a.download = u.name;
  a.rel = 'noopener';
  a.target = '_blank';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

export function ArtifactChip({ artifact }: { artifact: Artifact }) {
  const [busy, setBusy] = useState(false);
  const onClick = async () => {
    setBusy(true);
    try {
      await downloadArtifact(artifact.id);
    } catch (e) {
      void message.error(`下载失败：${errorText(e)}`);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="artifact-chip">
      <FileOutlined />
      <span className="artifact-name" title={artifact.name}>
        {artifact.name}
      </span>
      <span className="muted">{fmtBytes(artifact.size)}</span>
      <Tooltip title="下载（临时链接）">
        <Button size="small" type="text" icon={<DownloadOutlined />} loading={busy} onClick={onClick} />
      </Tooltip>
    </div>
  );
}
