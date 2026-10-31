export { asObject, hexOf, isHex64, jcsHash, sameJson, ymd } from './util.js';
import { canonicalize } from '@opm/shared';
export const canonicalSafe = (v: unknown) => {
  try {
    return canonicalize(v);
  } catch {
    return null;
  }
};
