import { useEffect } from 'react';
import { Alert, Button, Descriptions, Form, Input, InputNumber, Space, Typography, message } from 'antd';
import { SaveOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, tenantApi, type TenantSettings } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';

interface FormValues {
  name: string;
  daily_token_budget: number;
  max_concurrent_turns: number;
  default_role?: string;
  artifact_retention_days: number;
  llm_call_retention_days: number;
}

/** Tenant settings: quota, defaults and retention for the current tenant. */
export function TenantPage() {
  const qc = useQueryClient();
  const tenant = useQuery({ queryKey: ['tenant'], queryFn: tenantApi.get });
  const [form] = Form.useForm<FormValues>();

  useEffect(() => {
    if (!tenant.data) return;
    form.setFieldsValue({
      name: tenant.data.name,
      daily_token_budget: tenant.data.daily_token_budget ?? 0,
      max_concurrent_turns: tenant.data.max_concurrent_turns ?? 0,
      default_role: tenant.data.default_role ?? '',
      artifact_retention_days: tenant.data.artifact_retention_days ?? 0,
      llm_call_retention_days: tenant.data.llm_call_retention_days ?? 0,
    });
  }, [tenant.data, form]);

  const save = useMutation({
    mutationFn: (v: FormValues) => {
      const settings: TenantSettings = {
        name: v.name,
        daily_token_budget: v.daily_token_budget,
        max_concurrent_turns: v.max_concurrent_turns,
        default_role: v.default_role ?? '',
        artifact_retention_days: v.artifact_retention_days,
        llm_call_retention_days: v.llm_call_retention_days,
      };
      return tenantApi.update(settings);
    },
    onSuccess: () => {
      void message.success('设置已保存');
      void qc.invalidateQueries({ queryKey: ['tenant'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  if (tenant.isLoading) return <Loading />;
  if (tenant.isError) return <Alert type="error" showIcon message={errorText(tenant.error)} />;
  const t = tenant.data;
  if (!t) return null;

  return (
    <div className="page-card" style={{ maxWidth: 820 }}>
      <div className="page-toolbar">
        <Typography.Text strong>租户设置</Typography.Text>
        <span className="muted small">
          0 表示不限制或使用平台默认。修改会写入审计日志。
        </span>
      </div>
      <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)}>
        <Form.Item name="name" label="租户名称" rules={[{ required: true, message: '请输入名称' }]}>
          <Input />
        </Form.Item>
        <Space size={16} wrap align="start">
          <Form.Item label="每日 Token 预算" name="daily_token_budget" extra="达到后新的轮次会被拒绝。0 = 不限。">
            <InputNumber min={0} step={100_000} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item label="最大并发轮次" name="max_concurrent_turns" extra="0 = 平台默认。">
            <InputNumber min={0} max={10_000} style={{ width: 200 }} />
          </Form.Item>
        </Space>
        <Form.Item label="默认角色" name="default_role" extra="新会话未指定角色时使用；必须是已发布版本的角色。">
          <Input className="mono-input" style={{ maxWidth: 320 }} placeholder="例如 general" />
        </Form.Item>
        <Space size={16} wrap align="start">
          <Form.Item label="交付物保留天数" name="artifact_retention_days" extra="0 = 平台默认。">
            <InputNumber min={0} max={3650} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item label="LLM 调用记录保留天数" name="llm_call_retention_days" extra="调试器依赖这些记录；0 = 平台默认。">
            <InputNumber min={0} max={3650} style={{ width: 200 }} />
          </Form.Item>
        </Space>
        <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={save.isPending}>
          保存设置
        </Button>
      </Form>
      <Descriptions column={2} size="small" bordered style={{ marginTop: 24 }}>
        <Descriptions.Item label="租户 ID">{t.id}</Descriptions.Item>
        <Descriptions.Item label="状态">{t.status}</Descriptions.Item>
        <Descriptions.Item label="创建时间">{fmtTime(t.created_at)}</Descriptions.Item>
        <Descriptions.Item label="更新时间">{fmtTime(t.updated_at)}</Descriptions.Item>
      </Descriptions>
    </div>
  );
}