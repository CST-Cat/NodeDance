<script setup lang="ts">
defineProps<{ name: string }>()
const paths: Record<string, string> = {
  folder: 'M3 6h7l2 2h9v12H3z M3 6V4h7l2 2',
  file: 'M5 3h9l5 5v13H5z M14 3v6h5 M8 13h8 M8 17h6',
  refresh: 'M20 7v5h-5 M4 17v-5h5 M6 7a7 7 0 0 1 12-1l2 3 M4 15l2 3a7 7 0 0 0 12-1',
  terminal: 'M4 4h16v16H4z M7 8l3 3-3 3 M12 15h5',
  nodes: 'M4 4h16v6H4z M4 14h16v6H4z M7 7h.01 M7 17h.01 M11 7h6 M11 17h6',
  guest: 'M3 5h18v14H3z M3 8h18 M6 6.5h.01 M8 6.5h.01 M6 13.5s2-3.5 6-3.5 6 3.5 6 3.5-2 3.5-6 3.5-6-3.5-6-3.5z M12 12a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3',
  add: 'M12 5v14 M5 12h14',
  discovery: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M16 8l-3 5-5 3 3-5z',
  alerts: 'M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9 M10 21h4',
  settings: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M12 2v3 M12 19v3 M2 12h3 M19 12h3 M5 5l2 2 M17 17l2 2 M5 19l2-2 M17 7l2-2',
  logout: 'M9 4H4v16h5 M9 12h12 M17 8l4 4-4 4',
  menu: 'M4 6h16 M4 12h16 M4 18h16',
  close: 'M6 6l12 12 M6 18L18 6',
}
</script>

<template>
  <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="paths[name] || paths.nodes" /></svg>
</template>
