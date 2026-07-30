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
    disabled = false,
  }: {
    options: SegOption<string>[];
    value?: string;
    onselect?: (value: string) => void;
    vertical?: boolean;
    disabled?: boolean;
  } = $props();
</script>

<span class="seg" class:vertical class:disabled>
  {#each options as option (option.value)}
    <button
      type="button"
      class:on={option.value === value}
      {disabled}
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

  .seg.disabled {
    opacity: 0.45;
  }
</style>
