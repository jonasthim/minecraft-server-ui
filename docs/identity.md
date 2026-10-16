# Identity and access

On a fresh database, open the UI and create the first administrator. There are no default credentials. A permanent singleton bootstrap claim and account are committed in one SQLite transaction; separate service instances cannot both become the first administrator. SQLite triggers prevent removing or disabling the last enabled administrator even through concurrent requests.

Local passwords use salted Argon2id (64 MiB, three passes, four lanes). Passwords must be 10–128 characters. Five failed attempts lock a username for five minutes; a second bounded counter locks a source IP after 30 failures. The manager uses the direct peer IP and does not trust arbitrary forwarded headers.

Browser sessions use a cryptographically random 256-bit cookie token. SQLite stores its SHA-256 hash. Cookies are HttpOnly, SameSite=Lax, and Secure unless the operator explicitly enables insecure cookies for local HTTP. Sessions expire after seven idle days and at most 30 total days. Activity renews idle cookie expiry. Account disabling is checked on every request. The account page lists and revokes sessions. A local password change revokes other sessions; an administrator password reset revokes every session. An SSO account cannot change its password locally.

Every authenticated cookie mutation requires the `X-CSRF-Token` from `GET /api/v1/auth/me`. The token is bound to the current session. Unsafe requests also reject a foreign Origin; unauthenticated login/setup require JSON. The shared web API client obtains and sends this token automatically. Bearer requests use explicit credentials and do not require the cookie CSRF token.

Roles are global. Viewer reads server state; operator adds server operations; administrator manages users and settings. All feature routes must use `auth.RequireRole` with their minimum role, after `Service.Authenticate` and `Service.CSRF`. This also applies to event streams, files, and downloads.

## Personal tokens

Create tokens from Account → Personal API tokens. The full `mcui_…` secret is shown once; store it in a password manager or the consuming script's secret store. The database stores only its SHA-256 hash and a display prefix. Send `Authorization: Bearer mcui_…`. Token scopes independently cap the owner's current role:

| Scope | Allowed operations |
| --- | --- |
| `read` | Authorized GET/HEAD requests |
| `operate` | Reads and operations whose required role is viewer/operator |
| `admin` | Authorized feature operations, including administrator routes |

The browser limits offered scopes to the current role, and the API enforces the same rule. An account role reduction takes effect immediately on existing tokens. Scope defaults to read. Choose 1–3650 days, or zero for no expiry. Account → Revoke deletes a token immediately. Deleting or disabling its owner invalidates it. An invalid Authorization header never falls back to a cookie. Identity management, including token creation and revocation, requires a browser session even for an admin-scoped token; a leaked token cannot mint a replacement.

## Single sign-on

Authentication settings configure one OpenID Connect issuer, client, group claim, and group-to-role map. Register the displayed exact redirect URI with the provider. Discovery, code exchange, signature, issuer, audience, nonce, and PKCE verification use the reviewed `go-oidc` and `oauth2` libraries. Signed short-lived state cookies and consumed database flow records reject tampering, expired flows, and replay. Post-login redirects accept only local paths.

The highest mapped group wins; the default role can deny unmatched users. Account creation and role synchronization are configurable. Identity keys are issuer plus subject. Verified email may link an existing account; the advanced `link_unverified_email` setting defaults off and never permits an unverified link to an administrator. Keep local login enabled while testing SSO. Settings reject disabling both login methods. Empty submitted client secrets retain the saved secret; API responses always blank it. SQLite and the data directory must remain private because the provider secret is stored in the manager database.

Security mutations write audit events. Passwords, cookie secrets, bearer secrets, and OIDC client secrets are excluded from identity events. The shared audit recorder recursively redacts credential-named metadata. Audit viewing requires an administrator browser session. No credentials are included in user or session JSON.

The auth API fragment is `docs/api/auth.yaml`; foundation integration merges it into the published OpenAPI document before generating frontend types.
