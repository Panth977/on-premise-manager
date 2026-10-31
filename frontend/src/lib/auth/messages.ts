/**
 * User-facing text of a login error: the `auth/*` codes of the opm-cloud /auth/* errors (§21 #108).
 */
export function authMessage(e: unknown): string {
  const code = (e as { code?: string })?.code ?? '';
  switch (code) {
    case 'auth/invalid-credential':
    case 'auth/wrong-password':
    case 'auth/user-not-found':
    case 'auth/invalid-email':
    case 'auth/invalid-login-credentials':
      return 'Email or password is wrong';
    case 'auth/user-disabled':
      return 'This account is disabled';
    case 'auth/too-many-requests':
      return 'Too many attempts. Wait a moment and try again.';
    case 'auth/network-request-failed':
      return 'Cannot reach the login service. Check your connection.';
    case 'auth/invalid-refresh-token':
      return 'Your login expired. Log in again.';
    case 'auth/invalid-oidc-code':
      return 'The single sign-on login expired or was already used. Try again.';
    default:
      return (e as Error)?.message || 'Login failed';
  }
}
