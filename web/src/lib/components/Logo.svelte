<script lang="ts">
  let { size = 26 }: { size?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 76 76" fill="none" aria-label="WT logo">
  <line x1="38" y1="40" x2="38" y2="12" stroke="var(--accent)" stroke-width="5" stroke-linecap="round" />
  <line x1="38" y1="40" x2="60" y2="52" stroke="var(--accent)" stroke-width="5" stroke-linecap="round" />
  <line x1="38" y1="12" x2="60" y2="52" stroke="var(--accent)" stroke-width="2.5" stroke-linecap="round" opacity="0.35" />
  <circle cx="38" cy="12" r="6" fill="var(--accent)" />
  <circle cx="60" cy="52" r="5.5" fill="var(--accent)" />
  <circle cx="14" cy="30" r="3.5" fill="var(--accent)" opacity="0.35" />
  <circle cx="20" cy="60" r="3" fill="var(--accent)" opacity="0.25" />
  <circle cx="38" cy="40" r="7" fill="var(--text)" />
</svg>
