/** Where a variable / secret / file / container is used (shown before a remove). */
import { exprRefs, isExpr, type Config } from '@opm/shared';

export interface RefHit {
  path: string;
  value: string;
}
function valuesOf(config: Config, slugOf: (id: string) => string): RefHit[] {
  const out: RefHit[] = [];
  for (const s of config.sites) {
    const b = `sites[${s.siteSlug}]`;
    if (s.config.host) out.push({ path: `${b}.host`, value: s.config.host });
    for (const c of s.config.containers) {
      const cb = `${b}.containers[${slugOf(c.containerSlug)}]`;
      for (const e of c.env) if (e.value) out.push({ path: `${cb}.env[${e.name}]`, value: e.value });
      for (const p of c.ports) if (typeof p.hostPort === 'string') out.push({ path: `${cb}.ports[${p.portName}].hostPort`, value: p.hostPort });
      c.volumes.forEach((v, i) => v.mountPath && out.push({ path: `${cb}.volumes[${v.imageVolume ?? i}].mountPath`, value: v.mountPath }));
    }
    for (const h of s.config.edge.hosts) {
      out.push({ path: `${b}.edge.hosts[${h.hostname}]`, value: h.hostname });
      for (const part of ['set', 'add'] as const) for (const [k, v] of Object.entries(h.responseHeaders?.[part] ?? {})) out.push({ path: `${b}.edge.hosts[${h.hostname}].responseHeaders.${part}.${k}`, value: v });
    }
    for (const r of s.config.edge.routes) {
      const rb = `${b}.edge.routes[${r.routeId}]`;
      const a = r.action;
      if (a.type === 'redirect') out.push({ path: `${rb}.to`, value: a.to });
      else if (a.type === 'rewrite') out.push({ path: `${rb}.replacement`, value: a.replacement });
      else {
        for (const [k, v] of Object.entries(a.setHeaders)) out.push({ path: `${rb}.setHeaders.${k}`, value: v });
        if (a.rewrite) out.push({ path: `${rb}.rewrite.replacement`, value: a.rewrite.replacement });
      }
      for (const part of ['set', 'add'] as const) for (const [k, v] of Object.entries(r.responseHeaders?.[part] ?? {})) out.push({ path: `${rb}.responseHeaders.${part}.${k}`, value: v });
    }
  }
  return out;
}
function exprHits(config: Config, slugOf: (id: string) => string, test: (ref: string) => boolean): RefHit[] {
  return valuesOf(config, slugOf).filter((h) => {
    if (!isExpr(h.value)) return false;
    try {
      return exprRefs(h.value).some(test);
    } catch {
      return false;
    }
  });
}
export function variableRefs(config: Config, slug: string, slugOf: (id: string) => string): RefHit[] {
  return exprHits(config, slugOf, (r) => r === `var.${slug}`);
}
export function secretRefs(config: Config, slug: string, slugOf: (id: string) => string): RefHit[] {
  const out = exprHits(config, slugOf, (r) => r === `secret.${slug}`);
  for (const s of config.sites) {
    for (const c of s.config.containers) {
      const cb = `sites[${s.siteSlug}].containers[${slugOf(c.containerSlug)}]`;
      for (const e of c.env) if (e.secretRef === slug) out.push({ path: `${cb}.env[${e.name}]`, value: 'secretRef' });
      c.volumes.forEach((v, i) => v.source.kind === 'secret' && v.source.secretSlug === slug && out.push({ path: `${cb}.volumes[${v.imageVolume ?? i}]`, value: `secret mount ${v.mountPath ?? v.imageVolume ?? ''}` }));
    }
    for (const h of s.config.edge.hosts) {
      if (h.tls.mode === 'files' && h.tls.keySecretSlug === slug) out.push({ path: `sites[${s.siteSlug}].edge.hosts[${h.hostname}].tls`, value: 'TLS key secret' });
      if (h.tls.mode === 'acme' && h.tls.dnsTokenSecretSlug === slug) out.push({ path: `sites[${s.siteSlug}].edge.hosts[${h.hostname}].tls`, value: 'DNS token secret' });
    }
  }
  return out;
}
export function fileRefs(config: Config, slug: string, slugOf: (id: string) => string): RefHit[] {
  const out = exprHits(config, slugOf, (r) => r === `file.${slug}.path`);
  for (const s of config.sites) {
    for (const c of s.config.containers)
      c.volumes.forEach((v, i) => v.source.kind === 'file' && v.source.fileSlug === slug && out.push({ path: `sites[${s.siteSlug}].containers[${slugOf(c.containerSlug)}].volumes[${v.imageVolume ?? i}]`, value: `file mount ${v.mountPath ?? v.imageVolume ?? ''}` }));
    for (const h of s.config.edge.hosts) if (h.tls.mode === 'files' && h.tls.certFileSlug === slug) out.push({ path: `sites[${s.siteSlug}].edge.hosts[${h.hostname}].tls`, value: 'TLS certificate file' });
  }
  return out;
}
export function siteRefs(config: Config, slug: string, slugOf: (id: string) => string): RefHit[] {
  const out = exprHits(config, slugOf, (r) => r.startsWith(`site.${slug}.`));
  for (const s of config.sites) if (s.config.depends.some((d) => d.siteSlug === slug)) out.push({ path: `sites[${s.siteSlug}].depends`, value: `depends on ${slug}` });
  return out;
}
