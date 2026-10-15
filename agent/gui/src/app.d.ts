declare global {
  const __OPM_MOCK__: boolean;
  namespace App {}
  interface ImportMetaEnv {
    readonly VITE_MOCK?: string;
  }
}
export {};
