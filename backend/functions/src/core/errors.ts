import type { ErrorCode, ValidationIssue } from '@opm/shared';
import { ERROR_HTTP_STATUS } from '@opm/shared';

export class ApiError extends Error {
  constructor(
    public readonly code: ErrorCode,
    message: string,
    public readonly details: Record<string, unknown> = {},
    public readonly headers: Record<string, string> = {},
  ) {
    super(message);
  }
  get status() {
    return ERROR_HTTP_STATUS[this.code] ?? 500;
  }
}

export const E = {
  unauth: (message = 'not signed in', details: Record<string, unknown> = {}) => new ApiError('unauthenticated', message, details),
  forbidden: (message = 'forbidden', details: Record<string, unknown> = {}) => new ApiError('forbidden', message, details),
  notFound: (what = 'not found') => new ApiError('not-found', what),
  conflict: (message: string, details: Record<string, unknown> = {}) => new ApiError('conflict', message, details),
  gone: (message: string, details: Record<string, unknown> = {}) => new ApiError('gone', message, details),
  tooLarge: (message: string) => new ApiError('too-large', message),
  unavailable: (message: string, details: Record<string, unknown> = {}) => new ApiError('unavailable', message, details),
  stale: (message: string, details: Record<string, unknown> = {}) => new ApiError('stale', message, details),
  invalid: (errors: ValidationIssue[] | string, message?: string) => {
    const list: ValidationIssue[] = typeof errors === 'string' ? [{ path: '', code: 'V_TYPE', message: errors, severity: 'error' }] : errors;
    return new ApiError('invalid', message ?? list[0]?.message ?? 'invalid request', { errors: list });
  },
  /** one issue shortcut */
  bad: (path: string, code: string, message: string) => new ApiError('invalid', message, { errors: [{ path, code, message, severity: 'error' }] }),
};
