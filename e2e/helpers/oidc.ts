// Fake-OIDC route.fulfill helper (docs/e2e.md §G).
//
// Intercepts browser requests to a fake OIDC provider and fulfills them with
// canned responses (discovery document, JWKS, authorization endpoint, token
// endpoint). Used when the E2E server is configured with OIDC (SEMIPLANE_OIDC_*
// env vars) and the spec needs to drive the Authorization Code + PKCE flow
// without real credentials or network calls.
//
// Zero external network calls: the provider is mocked entirely via
// page.route.fulfill, never a real OIDC server.
//
// Example (when OIDC is wired in E2E):
//
//	await fakeOIDC(page, {
//	  issuer: 'http://localhost:9999/fake',
//	  clientId: 'test-client',
//	  code: 'auth-code-123',
//	  sub: 'oidc:alice-sub-42',
//	});
//	await page.goto('/api/v1/auth/oidc/start');
//	// browser is redirected to the mock authorization endpoint...
//
// Not used by Wave-1 specs (OIDC is unconfigured in E2E → the two OIDC routes
// answer 503 problem+json, which the auth spec asserts directly).

import { type Page } from '@playwright/test';

export interface FakeOIDCOptions {
  issuer: string;
  clientId: string;
  redirectURL: string;
  code: string;
  sub: string;
}

// Sets up route interception for a fake OIDC provider. Routes matched:
//   GET {issuer}/.well-known/openid-configuration → discovery doc
//   GET {issuer}/.well-known/jwks.json            → JWKS (empty keys; tokens
//                                                     are not validated here)
//   GET {issuer}/protocol/openid-connect/auth     → 302 back to redirectURL
//                                                     with state+code
//   POST {issuer}/protocol/openid-connect/token   → 200 {id_token: fake JWT}
//
// The id_token is an unsigned placeholder; the server's go-oidc verifier
// would reject it. This helper is useful for testing the redirect shape and
// state/nonce round-trip, not full ID-token validation. For full PKCE +
// signature validation, use a real go-oidc oidctest server (as internal/auth
// does in oidc_test.go).
export function fakeOIDC(page: Page, opts: FakeOIDCOptions): void {
  const { issuer, clientId, redirectURL, code, sub } = opts;

  const discovery = {
    issuer,
    authorization_endpoint: `${issuer}/protocol/openid-connect/auth`,
    token_endpoint: `${issuer}/protocol/openid-connect/token`,
    jwks_uri: `${issuer}/protocol/openid-connect/jwks`,
    response_types_supported: ['code'],
    grant_types_supported: ['authorization_code'],
    code_challenge_methods_supported: ['S256'],
    subject_types_supported: ['public'],
    id_token_signing_alg_values_supported: ['RS256'],
  };

  page.route(`${issuer}/.well-known/openid-configuration`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(discovery),
    });
  });

  page.route(`${issuer}/protocol/openid-connect/jwks`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ keys: [] }),
    });
  });

  page.route(`${issuer}/protocol/openid-connect/auth`, (route) => {
    const url = new URL(route.request().url());
    const state = url.searchParams.get('state') ?? 'fake-state';
    const redirect = new URL(redirectURL);
    redirect.searchParams.set('code', code);
    redirect.searchParams.set('state', state);
    void route.fulfill({
      status: 302,
      headers: { Location: redirect.toString() },
    });
  });

  page.route(`${issuer}/protocol/openid-connect/token`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        access_token: 'fake-access-token',
        token_type: 'Bearer',
        expires_in: 3600,
        id_token: 'fake.id.token',
      }),
    });
  });
}
