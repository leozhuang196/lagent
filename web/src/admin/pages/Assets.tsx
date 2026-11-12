import { rolesApi, skillsApi, workflowDefsApi, type RoleAsset, type SkillAsset, type WorkflowDef } from '../../api';
import { AssetDetailPage, AssetListPage, type AssetPageProps } from './AssetPages';
import { RoleEditor, SkillEditor, WorkflowEditor, emptyRole, emptySkill, emptyWorkflow } from './assetEditors';

const roles: AssetPageProps<RoleAsset> = {
  kind: '角色',
  base: '/admin/roles',
  api: rolesApi,
  nameOf: (r) => r.name,
  descOf: (r) => r.description,
  editor: ({ value, onChange }) => <RoleEditor value={value} onChange={onChange} />,
  createEmpty: emptyRole,
};

const skills: AssetPageProps<SkillAsset> = {
  kind: '技能',
  base: '/admin/skills',
  api: skillsApi,
  nameOf: (s) => s.name,
  descOf: (s) => s.description,
  editor: ({ value, onChange }) => <SkillEditor value={value} onChange={onChange} />,
  createEmpty: emptySkill,
};

const workflows: AssetPageProps<WorkflowDef> = {
  kind: '工作流模板',
  base: '/admin/workflow-defs',
  api: workflowDefsApi,
  nameOf: (w) => w.name,
  descOf: (w) => w.description,
  editor: ({ value, onChange }) => <WorkflowEditor value={value} onChange={onChange} />,
  createEmpty: emptyWorkflow,
};

export function RolesPage() {
  return <AssetListPage {...roles} />;
}

export function RoleDetailPage() {
  return <AssetDetailPage {...roles} />;
}

export function SkillsPage() {
  return <AssetListPage {...skills} />;
}

export function SkillDetailPage() {
  return <AssetDetailPage {...skills} />;
}

export function WorkflowDefsPage() {
  return <AssetListPage {...workflows} />;
}

export function WorkflowDefDetailPage() {
  return <AssetDetailPage {...workflows} />;
}