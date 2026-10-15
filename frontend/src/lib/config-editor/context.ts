import { getContext, setContext } from 'svelte';
import type { ConfigScope, DeploymentScope, TemplateScope } from './scope.svelte';

const KEY = Symbol('opm.scope');
export function setScope<T extends ConfigScope>(s: T): T {
  setContext(KEY, s);
  return s;
}
export function getScope(): TemplateScope | DeploymentScope {
  const s = getContext<TemplateScope | DeploymentScope>(KEY);
  if (!s) throw new Error('no config scope in context');
  return s;
}
export function getDeploymentScope(): DeploymentScope {
  const s = getScope();
  if (s.kind !== 'deployment') throw new Error('not a deployment scope');
  return s;
}
export function getTemplateScope(): TemplateScope {
  const s = getScope();
  if (s.kind !== 'template') throw new Error('not a template scope');
  return s;
}
