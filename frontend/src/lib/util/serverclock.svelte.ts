/**
 * §21 #92 the cloud's clock as seen from this browser. Every API response carries `X-OPM-Server-Time` (unix seconds);
 * the offset (server − browser) corrects every relative time ("Last seen 5 s ago", countdowns), so a browser whose
 * clock is wrong (the user's case: 5 h 30 min behind) never shows a server timestamp "in 5 h". Offsets under 5 s are
 * treated as 0 (the header has 1 s resolution + network delay).
 */
let offsetMs = $state(0);
let measured = $state(false);

export function noteServerTime(unixSec: number) {
  if (!Number.isFinite(unixSec) || unixSec <= 0) return;
  const off = unixSec * 1000 + 500 - Date.now();
  offsetMs = Math.abs(off) >= 5000 ? Math.round(off / 1000) * 1000 : 0;
  measured = true;
}
/** reads the header of an API response (no-op without it) */
export function noteResponse(res: Response) {
  const h = res.headers.get('X-OPM-Server-Time');
  if (h && /^\d{9,12}$/.test(h)) noteServerTime(Number(h));
}
export const serverClock = {
  /** server − browser clock in ms (0 when within 5 s) */
  get offsetMs() {
    return offsetMs;
  },
  get measured() {
    return measured;
  },
};
/** "now" on the cloud's clock */
export function serverNow(): number {
  return Date.now() + offsetMs;
}
