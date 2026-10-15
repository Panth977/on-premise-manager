import { errMessage } from '../api';

export type ToastKind = 'ok' | 'bad' | 'info' | 'warn';
export interface Toast {
  id: number;
  kind: ToastKind;
  title: string;
  detail?: string;
  /** ms; 0 = sticky until dismissed / replaced */
  ttl: number;
}
let nextId = 1;

class Toasts {
  list = $state<Toast[]>([]);
  push(kind: ToastKind, title: string, detail?: string, ttl = kind === 'bad' ? 8000 : 4500): number {
    const id = nextId++;
    this.list = [...this.list.slice(-4), { id, kind, title, detail, ttl }];
    if (ttl > 0) setTimeout(() => this.dismiss(id), ttl);
    return id;
  }
  /** replace an existing toast (progress toasts) */
  update(id: number, kind: ToastKind, title: string, detail?: string, ttl = 4500) {
    const i = this.list.findIndex((t) => t.id === id);
    if (i < 0) return this.push(kind, title, detail, ttl);
    this.list[i] = { id, kind, title, detail, ttl };
    if (ttl > 0) setTimeout(() => this.dismiss(id), ttl);
    return id;
  }
  ok(title: string, detail?: string) {
    return this.push('ok', title, detail);
  }
  info(title: string, detail?: string, ttl?: number) {
    return this.push('info', title, detail, ttl);
  }
  err(e: unknown, title = 'Failed') {
    return this.push('bad', title, errMessage(e));
  }
  dismiss(id: number) {
    this.list = this.list.filter((t) => t.id !== id);
  }
}
export const toasts = new Toasts();
