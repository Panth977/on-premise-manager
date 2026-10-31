// See https://svelte.dev/docs/kit/types#app.d.ts
declare global {
  namespace App {}
  interface ImportMetaEnv {
    readonly VITE_MOCK?: string;
    readonly VITE_OPM_API_BASE?: string;
    readonly VITE_OPM_GOOGLE_DOMAIN?: string;
    readonly VITE_EMULATOR_API_BASE?: string;
    readonly VITE_AUTH_EMULATOR_URL?: string;
    readonly VITE_EMULATOR_PROJECT?: string;
  }
}
export {};
