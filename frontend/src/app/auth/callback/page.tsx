'use client';

/**
 * OIDC redirect target for the web runtime. The web root layout completes
 * the authorization-code exchange on this route and then replaces the URL
 * with the original destination; this page itself renders nothing. The
 * desktop runtime never navigates here.
 */
export default function AuthCallbackPage() {
  return null;
}
