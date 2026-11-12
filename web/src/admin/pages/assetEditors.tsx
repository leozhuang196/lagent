import { Form, Input, InputNumber, Select, Space } from 'antd';
import type { PermissionMode, RoleAsset, SkillAsset, WorkflowDef } from '../../api';
import { JsonInput } from '../components/JsonInput';

/**
 * Editors keep everything loaded from the server and only replace the fields
 * shown, so saving a draft never drops fields this UI does not know yet.
 */

export const permissionModes: { value: PermissionMode; label: string }[] = [
  { value: 'ask_all', label: '每次询问（ask_all）' },
  { value: 'ask_external', label: '仅对外操作询问（ask_external）' },
  { value: 'auto', label: '自动执行（auto）' },
];

export function emptyRole(key = ''): RoleAsset {
  return {
    id: '',
    tenant_id: '',
    version: 0,
    created_at: '',
    updated_at: '',
    key,
    ver: 0,
    status: 'draft',
    name: key,
    description: '',
    prompt: '',
    tools: [],
    skills: [],
    memory_spaces: [],
    default_permission_mode: 'ask_all',
  };
}

export function emptySkill(key = ''): SkillAsset {
  return {
    id: '',
    tenant_id: '',
    version: 0,
    created_at: '',
    updated_at: '',
    key,
    ver: 0,
    status: 'draft',
    name: key,
    description: '',
    body: '',
    attachments: [],
    priority: 0,
  };
}

export function emptyWorkflow(key = ''): WorkflowDef {
  return {
    id: '',
    tenant_id: '',
    version: 0,
    created_at: '',
    updated_at: '',
    key,
    ver: 0,
    status: 'draft',
    name: key,
    description: '',
    role: '',
    params: [],
    steps: [],
    timeout_sec: 0,
    max_parallel: 0,
    token_budget: 0,
  };
}

export function RoleEditor({
  value,
  onChange,
  extraToolOptions,
}: {
  value: RoleAsset;
  onChange: (v: RoleAsset) => void;
  extraToolOptions?: string[];
}) {
  const set = <K extends keyof RoleAsset>(k: K, v: RoleAsset[K]) => onChange({ ...value, [k]: v });
  return (
    <Form layout="vertical" size="small">
      <Form.Item label="名称" required>
        <Input value={value.name} onChange={(e) => set('name', e.target.value)} />
      </Form.Item>
      <Form.Item label="描述">
        <Input value={value.description ?? ''} onChange={(e) => set('description', e.target.value)} />
      </Form.Item>
      <Form.Item label="系统提示词（prompt）" required extra="assembly 的核心内容；此处保存的是模板原文。">
        <Input.TextArea className="asset-editor" rows={10} value={value.prompt} onChange={(e) => set('prompt', e.target.value)} />
      </Form.Item>
      <Form.Item label="工具" extra="工具名、mcp:<服务器名>（该服务器全部工具）或 *（全部已注册工具）。角色只做收窄。">
        <Select
          mode="tags"
          value={value.tools ?? []}
          onChange={(v) => set('tools', v as string[])}
          tokenSeparators={[',', ' ']}
          options={(extraToolOptions ?? []).map((t) => ({ value: t, label: t }))}
          placeholder="选择或输入工具名"
        />
      </Form.Item>
      <Form.Item label="技能">
        <Select mode="tags" value={value.skills ?? []} onChange={(v) => set('skills', v as string[])} tokenSeparators={[',', ' ']} />
      </Form.Item>
      <Form.Item label="记忆空间">
        <Select mode="tags" value={value.memory_spaces ?? []} onChange={(v) => set('memory_spaces', v as string[])} tokenSeparators={[',', ' ']} />
      </Form.Item>
      <Form.Item label="模板">
        <Select mode="tags" value={value.templates ?? []} onChange={(v) => set('templates', v as string[])} tokenSeparators={[',', ' ']} />
      </Form.Item>
      <Space size={12} wrap>
        <Form.Item label="默认权限模式">
          <Select
            style={{ width: 220 }}
            value={value.default_permission_mode}
            onChange={(v) => set('default_permission_mode', v)}
            options={permissionModes}
          />
        </Form.Item>
        <Form.Item label="effort">
          <Select
            style={{ width: 140 }}
            allowClear
            value={value.effort || undefined}
            onChange={(v) => set('effort', v)}
            options={['low', 'medium', 'high', 'max'].map((e) => ({ value: e, label: e }))}
          />
        </Form.Item>
        <Form.Item label="最大迭代轮数">
          <InputNumber min={0} value={value.max_iterations ?? 0} onChange={(v) => set('max_iterations', v ?? 0)} />
        </Form.Item>
        <Form.Item label="Token 预算（0=默认）">
          <InputNumber min={0} step={1000} value={value.token_budget ?? 0} onChange={(v) => set('token_budget', v ?? 0)} />
        </Form.Item>
      </Space>
      <Space size={12} wrap>
        <Form.Item label="模型">
          <Input style={{ width: 200 }} placeholder="留空使用租户默认" value={value.model ?? ''} onChange={(e) => set('model', e.target.value)} />
        </Form.Item>
        <Form.Item label="备用模型">
          <Input style={{ width: 200 }} value={value.fallback_model ?? ''} onChange={(e) => set('fallback_model', e.target.value)} />
        </Form.Item>
      </Space>
    </Form>
  );
}

export function SkillEditor({ value, onChange }: { value: SkillAsset; onChange: (v: SkillAsset) => void }) {
  const set = <K extends keyof SkillAsset>(k: K, v: SkillAsset[K]) => onChange({ ...value, [k]: v });
  return (
    <Form layout="vertical" size="small">
      <Form.Item label="名称" required>
        <Input value={value.name} onChange={(e) => set('name', e.target.value)} />
      </Form.Item>
      <Form.Item label="描述" required extra="名称和描述始终在上下文中；正文只通过 load_skill 按需加载。">
        <Input value={value.description} onChange={(e) => set('description', e.target.value)} />
      </Form.Item>
      <Form.Item label="正文" required>
        <Input.TextArea className="asset-editor" rows={14} value={value.body} onChange={(e) => set('body', e.target.value)} />
      </Form.Item>
      <Form.Item label="附件（artifact id）">
        <Select mode="tags" value={value.attachments ?? []} onChange={(v) => set('attachments', v as string[])} tokenSeparators={[',']} />
      </Form.Item>
      <Form.Item label="优先级">
        <InputNumber value={value.priority ?? 0} onChange={(v) => set('priority', v ?? 0)} />
      </Form.Item>
    </Form>
  );
}

export function WorkflowEditor({ value, onChange }: { value: WorkflowDef; onChange: (v: WorkflowDef) => void }) {
  const set = <K extends keyof WorkflowDef>(k: K, v: WorkflowDef[K]) => onChange({ ...value, [k]: v });
  return (
    <Form layout="vertical" size="small">
      <Form.Item label="名称" required>
        <Input value={value.name} onChange={(e) => set('name', e.target.value)} />
      </Form.Item>
      <Form.Item label="描述">
        <Input value={value.description ?? ''} onChange={(e) => set('description', e.target.value)} />
      </Form.Item>
      <Space size={12} wrap>
        <Form.Item label="默认角色">
          <Input style={{ width: 180 }} value={value.role ?? ''} onChange={(e) => set('role', e.target.value)} />
        </Form.Item>
        <Form.Item label="超时（秒，0=默认）">
          <InputNumber min={0} value={value.timeout_sec ?? 0} onChange={(v) => set('timeout_sec', v ?? 0)} />
        </Form.Item>
        <Form.Item label="最大并行">
          <InputNumber min={0} value={value.max_parallel ?? 0} onChange={(v) => set('max_parallel', v ?? 0)} />
        </Form.Item>
        <Form.Item label="Token 预算">
          <InputNumber min={0} step={1000} value={value.token_budget ?? 0} onChange={(v) => set('token_budget', v ?? 0)} />
        </Form.Item>
      </Space>
      <Form.Item
        label="参数（params）"
        extra='例如 [{"name":"week","type":"period","label":"周期","default":"last_week"}]'
      >
        <JsonInput key={`params-${value.id}-${value.ver}`} value={(value.params ?? []) as never} onChange={(v) => set('params', (v ?? []) as unknown as WorkflowDef['params'])} rows={6} />
      </Form.Item>
      <Form.Item
        label="步骤（steps）"
        extra='每个步骤：{id, instruction, depends_on?, fan_out?, retries?, timeout_sec?, on_failure?, side_effect?, role?, sources?}'
      >
        <JsonInput key={`steps-${value.id}-${value.ver}`} value={(value.steps ?? []) as never} onChange={(v) => set('steps', (v ?? []) as unknown as WorkflowDef['steps'])} rows={16} />
      </Form.Item>
    </Form>
  );
}