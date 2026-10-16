import type { components, operations } from '../../api/schema';

export type User = components['schemas']['AuthUser'];
export type Status = components['schemas']['AuthStatus'];
export type Session = components['schemas']['AuthSession'];
export type Token = components['schemas']['AuthToken'];
export type OIDC = components['schemas']['AuthOIDCSettings'];
export type Settings = components['schemas']['AuthSettings'];
export type AuditEntry = components['schemas']['AuditEntry'];

type JSONBody<Response> = Response extends { content: { 'application/json': infer Body } } ? Body : never;
export type APIResponse<Operation extends keyof operations, Code extends keyof operations[Operation]['responses']> = JSONBody<operations[Operation]['responses'][Code]>;
export type APIRequest<Operation extends keyof operations> = operations[Operation] extends { requestBody: { content: { 'application/json': infer Body } } } ? Body : never;

export function isRole(value: string | null): value is User['role'] {
  return value === 'viewer' || value === 'operator' || value === 'admin';
}
