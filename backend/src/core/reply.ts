import type { Readable } from 'node:stream';
/** A non-JSON (or custom status/header) response. Handlers return it; JSON objects are wrapped by the router. */
export class Reply {
  constructor(
    public status: number,
    public headers: Record<string, string>,
    public body: Buffer | string | Readable | null,
  ) {}
}
