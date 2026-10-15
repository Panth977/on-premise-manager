// Cloud API as the admin human (Firebase Auth ID token).
import { createCloudClient, type CloudClient } from '../../../shared/dist/cloud-api.js';
import { CFG } from './env.js';
import { log } from './report.js';

interface Tok { idToken: string; refreshToken: string; at: number }
let tok: Tok | null = null;

function authUrl(path: string, host = 'identitytoolkit.googleapis.com'): string {
  return CFG.authEmulator ? `http://${CFG.authEmulator}/${host}${path}?key=${CFG.firebaseApiKey}` : `https://${host}${path}?key=${CFG.firebaseApiKey}`;
}

export async function signIn(email = CFG.adminEmail, password = CFG.adminPassword): Promise<Tok> {
  const r = await fetch(authUrl('/v1/accounts:signInWithPassword'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password, returnSecureToken: true }),
  });
  const j = (await r.json()) as { idToken?: string; refreshToken?: string; error?: { message: string } };
  if (!r.ok || !j.idToken) throw new Error(`signInWithPassword ${email}: ${r.status} ${j.error?.message ?? ''}`);
  tok = { idToken: j.idToken, refreshToken: j.refreshToken!, at: Date.now() };
  log(`signed in as ${email}`);
  return tok;
}

async function idToken(): Promise<string> {
  if (!tok) await signIn();
  if (Date.now() - tok!.at > 45 * 60 * 1000) {
    const r = await fetch(authUrl('/v1/token', 'securetoken.googleapis.com'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: `grant_type=refresh_token&refresh_token=${encodeURIComponent(tok!.refreshToken)}`,
    });
    const j = (await r.json()) as { id_token?: string; refresh_token?: string };
    if (j.id_token) tok = { idToken: j.id_token, refreshToken: j.refresh_token ?? tok!.refreshToken, at: Date.now() };
    else await signIn();
  }
  return tok!.idToken;
}

export const cloud: CloudClient = createCloudClient(CFG.apiBase, async () => `Bearer ${await idToken()}`);
/** raw call (for endpoints where we need the Response, e.g. install script with the install token) */
export async function cloudRaw(path: string, init: RequestInit = {}, auth: 'human' | string = 'human'): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set('Authorization', auth === 'human' ? `Bearer ${await idToken()}` : `Bearer ${auth}`);
  return fetch(CFG.apiBase + path, { ...init, headers });
}
