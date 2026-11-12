import { Form, Input, Modal, Select, message } from 'antd';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, rolesApi, sessionsApi, type Session } from '../api';

interface Props {
  open: boolean;
  onClose(): void;
  onCreated(s: Session): void;
}

interface FormValues {
  title?: string;
  role?: string;
}

export function NewSessionModal({ open, onClose, onCreated }: Props) {
  const [form] = Form.useForm<FormValues>();
  const qc = useQueryClient();
  // Roles are member-readable; only published versions are selectable here
  // (drafts belong to the playground, not to ordinary users).
  const roles = useQuery({ queryKey: ['assets', '/roles'], queryFn: () => rolesApi.list(), enabled: open });
  const create = useMutation({
    mutationFn: (v: FormValues) => sessionsApi.create({ title: v.title?.trim() || undefined, role: v.role?.trim() || undefined }),
    onSuccess: (s) => {
      void qc.invalidateQueries({ queryKey: ['sessions'] });
      form.resetFields();
      onCreated(s);
    },
    onError: (e) => void message.error(`创建失败：${errorText(e)}`),
  });
  return (
    <Modal
      title="新会话"
      open={open}
      onCancel={onClose}
      okText="创建"
      cancelText="取消"
      confirmLoading={create.isPending}
      onOk={() => form.submit()}
      destroyOnClose
    >
      <Form form={form} layout="vertical" onFinish={(v) => create.mutate(v)} preserve={false}>
        <Form.Item name="title" label="标题">
          <Input placeholder="可选，例如：周报整理" maxLength={120} autoFocus />
        </Form.Item>
        <Form.Item name="role" label="角色" extra="留空使用租户默认角色；只有已发布版本的角色可以开新会话。">
          <Select
            allowClear
            showSearch
            placeholder="选择角色（默认）"
            loading={roles.isLoading}
            options={(roles.data ?? [])
              .filter((r) => r.latest_ver > 0)
              .map((r) => ({
                value: r.key,
                label: `${r.latest?.name || r.key}（${r.key} · v${r.latest_ver}）`,
              }))}
            optionFilterProp="label"
          />
        </Form.Item>
      </Form>
    </Modal>
  );
}
