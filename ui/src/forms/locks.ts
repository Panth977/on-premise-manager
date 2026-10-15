/** Lock reasons shown by Field `lock` (§21 #102): the same words in every form. */
export const LOCK_CLOUD_ONLY = 'Cloud only — change it in the cloud console (V_LOCAL_DEFINITION)';
export const LOCK_FIXED = 'Fixed after create';
export const LOCK_CONTAINERS = 'Cloud only — containers are added, renamed and re-imaged in the cloud console';
/** §21 #113: a container's image is picked (catalog version, docker reference or host image), not typed */
export const LOCK_IMAGE_PICKER = 'Pick it with “Change image…” (a catalog version, a docker reference or an image on this host)';
