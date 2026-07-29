<script lang="ts">
  interface SegOption<Value extends string> {
    value: Value;
    label: string;
  }

  let {
    options,
    value = $bindable(),
    onselect,
    vertical = false,
  }: {
    options: SegOption<string>[];
    value?: string;
    onselect?: (value: string) => void;
    vertical?: boolean;
  } = $props();
</script>

<span class="seg" class:vertical>
  {#each options as option (option.value)}
    <button
      type="button"
      class:on={option.value === value}
      onclick={() => {
        value = option.value;
        onselect?.(option.value);
      }}
    >
      {option.label}
    </button>
  {/each}
</span>

<style>
  .seg.vertical {
    flex-direction: column;
    align-items: stretch;
  }

  .seg.vertical button + button {
    border-left: none;
    border-top: 1px solid var(--border);
  }
</style>
