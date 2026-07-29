<script lang="ts">
  let { size = 26 }: { size?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 76 76" fill="none" aria-label="WT logo">
  <circle cx="38" cy="38" r="32" stroke="var(--accent)" stroke-width="5" fill="none" />
  <line x1="38" y1="38" x2="38" y2="19" stroke="var(--accent)" stroke-width="5.5" stroke-linecap="round" />
  <line x1="38" y1="38" x2="52" y2="46" stroke="var(--accent)" stroke-width="5.5" stroke-linecap="round" />
  <circle cx="38" cy="38" r="5" fill="var(--accent)" />
</svg>
