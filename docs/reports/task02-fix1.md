# Task 02 — review round 1 corrections

Status: implemented; awaiting integrator review and ledger decision. No commits or staging performed.

## Generated frontend contracts

Replaced handwritten identity DTOs across AuthProvider, AccountPage, OIDCSettingsPage, UsersPage, AuditPage, and the shared API client with aliases to generated `components` and `operations` in `web/src/api/schema.d.ts`. The feature's `types.ts` defines only schema aliases, operation request/response extraction helpers, and a role type guard. Form request payloads use TypeScript `satisfies` against generated operation request bodies. Role and token-scope selections retain the generated enum types. No generated schema files were edited by this package.

## OIDC mutation audit

OIDC provisioning writes one `user.create` event when it creates the account, recording the assigned role, provider issuer, subject, and `source: oidc_provider`. Role synchronization writes `user.role.sync` with `previous_role` and `new_role` after a successful change. Unchanged roles do not generate another mutation event. Mutation events are recorded before session creation, so a later session failure does not discard the history of the account mutation.

OIDC callback events are attributed to the verified OIDC user rather than an absent session or unrelated browser account. Login and identity-link events preserve their structured method/issuer metadata. The default service auditor now retains supplied details and instance attribution. Both audit implementations use the same recursive credential redactor; raw token claims are never copied into audit details.

## Acceptance evidence

The two regression tests were run against the previous implementation and failed with `missing verified identity attribution for auth.login` and `audit attribution discarded`. After the correction they pass:

- `TestOIDCSignedLoginAuditsAccountAndRoleTransitions` exercises a real HTTP authorization flow through the controlled RSA-signed OIDC provider: account creation, viewer-to-operator role synchronization, unchanged repeat login, and denied group. It checks exactly one account creation event, exactly one role transition, three login events, verified user attribution, provider provenance, previous/new roles, and absence of credential values from persisted audit JSON.
- `TestServiceAuditorPreservesDetailsAndRedactsCredentials` checks preservation of actor, target, instance, role transition metadata, and recursive credential redaction.

Commands and results:

- `go test ./internal/auth ./internal/audit -count=1` — passed (51 auth tests).
- `go test -race ./cmd/minecraft-ui ./internal/api ./internal/config ./internal/db ./internal/domain ./internal/auth ./internal/audit` — passed.
- `npm --prefix web run typecheck` — passed.
- `npm run build` in `web` — passed.
- `npm run lint` in `web` — passed.

These changes address the two reported findings. This report does not claim real external-provider or release qualification, nor change the integrator-owned package status.
