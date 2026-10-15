<script lang="ts" module>
  // Lucide-style 24×24 stroke icons (only what the GUI uses; inline to keep the embedded bundle small)
  const P: Record<string, string> = {
    check: 'M20 6 9 17l-5-5',
    x: 'M18 6 6 18M6 6l12 12',
    alert: 'M12 9v4m0 4h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z',
    info: 'M12 16v-4m0-4h.01M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z',
    eye: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Zm10 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z',
    eyeOff: 'M9.9 4.2A10 10 0 0 1 12 4c6.4 0 10 8 10 8a17 17 0 0 1-2.2 3.2M6.6 6.6A17 17 0 0 0 2 12s3.6 8 10 8a9.7 9.7 0 0 0 5.4-1.6M2 2l20 20M14.1 14.1a3 3 0 0 1-4.2-4.2',
    copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
    refresh: 'M21 12a9 9 0 0 1-15.5 6.2L3 16m0 5v-5h5M3 12A9 9 0 0 1 18.5 5.8L21 8m0-5v5h-5',
    download: 'M12 3v12m0 0-4-4m4 4 4-4M4 17v3h16v-3',
    upload: 'M12 15V3m0 0L8 7m4-4 4 4M4 17v3h16v-3',
    chevronDown: 'm6 9 6 6 6-6',
    chevronRight: 'm9 6 6 6-6 6',
    chevronLeft: 'm15 6-6 6 6 6',
    menu: 'M4 6h16M4 12h16M4 18h16',
    plus: 'M12 5v14M5 12h14',
    minus: 'M5 12h14',
    braces: 'M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1M16 21h1a2 2 0 0 0 2-2v-5a2 2 0 0 1 2-2 2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1',
    list: 'M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01',
    trash: 'M3 6h18M8 6V4h8v2m-9 0 1 14h8l1-14',
    edit: 'M4 20h4L19 9l-4-4L4 16v4Zm9-13 4 4',
    arrowUp: 'M12 19V5m-6 6 6-6 6 6',
    arrowRight: 'M5 12h14m-6-6 6 6-6 6',
    arrowDown: 'M12 5v14m6-6-6 6-6-6',
    lock: 'M6 11h12v10H6zM8 11V7a4 4 0 0 1 8 0v4',
    globe: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20ZM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20Z',
    server: 'M3 4h18v7H3zM3 13h18v7H3zM7 7.5h.01M7 16.5h.01',
    external: 'M14 4h6v6m0-6-9 9M18 14v6H4V6h6',
    play: 'm7 4 13 8-13 8V4Z',
    fastForward: 'M13 19V5l9 7-9 7ZM2 19V5l9 7-9 7Z',
    eraser: 'm7 21-4.3-4.3a1 1 0 0 1 0-1.4l10-10a1 1 0 0 1 1.4 0l5.6 5.6a1 1 0 0 1 0 1.4L13 21M22 21H7M5 11l9 9',
    folderPlus: 'M12 10v6m-3-3h6M3 6a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z',
    filePlus: 'M14 3H6v18h12V7l-4-4Zm0 0v4h4M12 11v6m-3-3h6',
    stop: 'M6 6h12v12H6z',
    sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10ZM12 1v2m0 18v2M4.2 4.2l1.4 1.4m12.8 12.8 1.4 1.4M1 12h2m18 0h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4',
    moon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z',
    monitor: 'M3 4h18v12H3zM8 20h8M12 16v4',
    logout: 'M9 21H4V3h5m7 14 5-5-5-5m5 5H9',
    user: 'M20 21a8 8 0 0 0-16 0M12 13a5 5 0 1 0 0-10 5 5 0 0 0 0 10Z',
    search: 'm21 21-4.3-4.3M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14Z',
    file: 'M14 3H6v18h12V7l-4-4Zm0 0v4h4',
    clock: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20Zm0-16v6l4 2',
    wifi: 'M5 12.5a10 10 0 0 1 14 0M8.5 16a5 5 0 0 1 7 0M2 9a15 15 0 0 1 20 0M12 20h.01',
    wifiOff: 'M2 2l20 20M8.5 16a5 5 0 0 1 7 0M5 12.5a10 10 0 0 1 4.7-2.7M14.6 9.9A10 10 0 0 1 19 12.5M2 9a15 15 0 0 1 4.2-2.8M10.7 5.1A15 15 0 0 1 22 9M12 20h.01',
    layers: 'm12 2 10 5-10 5L2 7l10-5Zm-10 10 10 5 10-5M2 17l10 5 10-5',
    box: 'M21 8 12 3 3 8v8l9 5 9-5V8ZM3 8l9 5 9-5M12 13v8',
    key: 'M15 7a4 4 0 1 1-3.9 5H9v2H7v2H4v-3l6.1-6.1A4 4 0 0 1 15 7Zm1 1h.01',
    variable: 'M8 21s-4-3-4-9 4-9 4-9m8 0s4 3 4 9-4 9-4 9M9 9l6 6m0-6-6 6',
    sliders: 'M4 21v-7m0-4V3m8 18v-9m0-4V3m8 18v-5m0-4V3M1 14h6m2-6h6m2 8h6',
    diff: 'M18 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM6 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6Zm7-3h3a2 2 0 0 1 2 2v7M11 18H8a2 2 0 0 1-2-2V9',
    branch: 'M6 3v12M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM18 9a9 9 0 0 1-9 9',
    commit: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM3 12h6m6 0h6',
    history: 'M3 12a9 9 0 1 0 3-6.7L3 8m0-5v5h5m4-1v5l3 2',
    archive: 'M3 4h18v4H3zM5 8v12h14V8M10 12h4',
    activity: 'M22 12h-4l-3 9L9 3l-3 9H2',
    bug: 'M8 2l1.9 1.9M16 2l-1.9 1.9M9 7.1V6a3 3 0 0 1 6 0v1.1M12 20a6 6 0 0 1-6-6v-3a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v3a6 6 0 0 1-6 6Zm0 0v-9M6 13H2m20 0h-4M3 21l3-3m15 3-3-3M3 7l3 2m15-2-3 2',
    gauge: 'm12 14 4-4M3.3 19a10 10 0 1 1 17.4 0',
    cog: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1Z',
    send: 'm22 2-7 20-4-9-9-4 20-7Zm0 0L11 13',
    swap: 'M7 4 3 8l4 4M3 8h14m0 12 4-4-4-4m4 4H7',
    inbox: 'M22 12h-6l-2 3h-4l-2-3H2m3.5-7h13L22 12v7H2v-7l3.5-7Z',
    shield: 'M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z',
    grid: 'M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z',
    link: 'M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7',
    zap: 'M13 2 3 14h9l-1 8 10-12h-9l1-8Z',
    dots: 'M12 12h.01M19 12h.01M5 12h.01',
    // round T: operator toolbox
    terminal: 'm4 17 6-6-6-6m8 14h8',
    logs: 'M14 3H6v18h12V7l-4-4Zm0 0v4h4M9 13h6M9 17h6M9 9h1',
    chart: 'M3 3v18h18M7 15l4-4 3 3 6-6',
    network: 'M9 2h6v6H9zM2 16h6v6H2zM16 16h6v6h-6zM12 8v4M5 16v-4h14v4',
    hardDrive: 'M22 12H2m3.5-6.9L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.8 4H7.2a2 2 0 0 0-1.7 1.1ZM6 16h.01M10 16h.01',
    folder: 'M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1Z',
    wrench: 'M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.8-3.8a6 6 0 0 1-7.9 7.9l-6.9 6.9a2.1 2.1 0 0 1-3-3l6.9-6.9a6 6 0 0 1 7.9-7.9Z',
    container: 'M22 7.7c0-.6-.4-1.2-.8-1.5l-6.3-3.9a1.7 1.7 0 0 0-1.7 0l-10.3 6c-.5.2-.9.8-.9 1.4v6.6c0 .5.4 1.2.8 1.5l6.3 3.9a1.7 1.7 0 0 0 1.7 0l10.3-6c.5-.3.9-1 .9-1.5ZM10 21.9V14L2.1 9.1M10 14l11.9-6.9M14 19.8v-8.1M18 17.5V9.4',
    pause: 'M7 4h3v16H7zM14 4h3v16h-3z',
    cpu: 'M6 6h12v12H6zM9 9h6v6H9zM9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4',
    maximize: 'M8 3H3v5M21 8V3h-5M16 21h5v-5M3 16v5h5',
    paste: 'M9 3h6v4H9zM15 5h3v16H6V5h3',
    wrap: 'M3 6h18M3 12h15a3 3 0 1 1 0 6h-4m0 0 2-2m-2 2 2 2M3 18h7',
    fontSize: 'M4 7V4h16v3M9 20h6M12 4v16',
    toBottom: 'M12 3v12m0 0-5-5m5 5 5-5M5 21h14',
    rotate: 'M3 12a9 9 0 1 0 3-6.7L3 8m0-5v5h5',
    power: 'M12 2v10M18.4 6.6a9 9 0 1 1-12.8 0',
    filter: 'M3 4h18l-7 8v6l-4 2v-8Z',
    pulse: 'M22 12h-4l-3 9L9 3l-3 9H2',
    // §21 #97 dev tools
    database: 'M4 5c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3Zm0 0v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
    plug: 'M9 2v6m6-6v6M6 8h12v3a6 6 0 0 1-12 0V8Zm6 9v5',
    radio: 'M4.9 19.1a10 10 0 0 1 0-14.2m2.8 2.9a6 6 0 0 0 0 8.4m8.6 0a6 6 0 0 0 0-8.4m2.8-2.9a10 10 0 0 1 0 14.2M12 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z',
    table: 'M3 4h18v16H3zM3 10h18M3 15h18M9 10v10M15 10v10',
    columns: 'M3 4h18v16H3zM12 4v16',
    code: 'm16 18 6-6-6-6M8 6l-6 6 6 6',
    keyRound: 'M2 18v3h3l6.6-6.6a5.5 5.5 0 1 0-3-3L2 18Zm14.5-11.5h.01',
    flame: 'M8.5 14.5A2.5 2.5 0 0 0 11 12c0-1.4-.5-2-1-3-1.1-2.1-.2-4 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.2.4-2.3 1-3.3.3 1.6 1.4 2.8 2.5 2.8Z',
    save: 'M5 3h11l5 5v13H3V3h2Zm2 0v6h8V3M7 21v-7h10v7',
    folderOpen: 'M3 19V5h6l2 2h8v3M3 19l3-8h16l-3 8H3Z',
    tree: 'M6 3v12a3 3 0 0 0 3 3h3M6 9h6m0-3h9v6h-9zM12 15h9v6h-9z',
    hash: 'M4 9h16M4 15h16M10 3 8 21M16 3l-2 18',
    timer: 'M10 2h4M12 14l3-3M12 22a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z',
    sort: 'M7 4v16m0 0-3-3m3 3 3-3M17 20V4m0 0-3 3m3-3 3 3',
    split: 'M3 4h18v16H3zM3 13h18',
  };
  export type IconName = keyof typeof P;
</script>

<script lang="ts">
  let { name, size = 16, label, class: cls = '' }: { name: string; size?: number; label?: string; class?: string } = $props();
</script>

<svg
  class="ico {cls}"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
  role={label ? 'img' : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : 'true'}><path d={P[name] ?? P.dots} /></svg>

<style>
  .ico {
    flex: none;
    display: inline-block;
    vertical-align: -0.15em;
  }
</style>
