import { errMsg } from './api';

export type ToastKind = 'success' | 'error' | 'info' | 'warning';
export interface ToastItem {
  id: number;
  kind: ToastKind;
  title: string;
  detail?: string;
  timeout: number;
}

class Toasts {
  items: ToastItem[] = $state([]);
  #n = 0;
  push(kind: ToastKind, title: string, detail?: string, timeout = kind === 'error' ? 8000 : 4000) {
    const id = ++this.#n;
    this.items.push({ id, kind, title, detail, timeout });
    if (this.items.length > 5) this.items.splice(0, this.items.length - 5);
    if (timeout > 0) setTimeout(() => this.dismiss(id), timeout);
    return id;
  }
  dismiss(id: number) {
    const i = this.items.findIndex((t) => t.id === id);
    if (i >= 0) this.items.splice(i, 1);
  }
  success(title: string, detail?: string) {
    return this.push('success', title, detail);
  }
  info(title: string, detail?: string) {
    return this.push('info', title, detail);
  }
  warning(title: string, detail?: string) {
    return this.push('warning', title, detail);
  }
  /** error toast with the API message */
  error(e: unknown, title?: string) {
    const msg = errMsg(e);
    return title ? this.push('error', title, msg) : this.push('error', msg);
  }
}
export const toast = new Toasts();
