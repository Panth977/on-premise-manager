export type ThemeMode = 'system' | 'light' | 'dark';
class Theme {
  mode: ThemeMode = $state('system');
  constructor() {
    if (typeof localStorage !== 'undefined') {
      try {
        const t = localStorage.getItem('opm.theme');
        if (t === 'light' || t === 'dark') this.mode = t;
      } catch {
        /* ignore */
      }
    }
  }
  set(m: ThemeMode) {
    this.mode = m;
    try {
      if (m === 'system') localStorage.removeItem('opm.theme');
      else localStorage.setItem('opm.theme', m);
    } catch {
      /* ignore */
    }
    if (m === 'system') delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = m;
  }
  cycle() {
    this.set(this.mode === 'system' ? 'light' : this.mode === 'light' ? 'dark' : 'system');
  }
}
export const theme = new Theme();
