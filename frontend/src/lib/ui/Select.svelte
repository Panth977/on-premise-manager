<script lang="ts" module>
  export interface SelectOption {
    value: string;
    label: string;
    /** second line, muted */
    sub?: string;
    /** small badge on the right */
    badge?: string;
    disabled?: boolean;
    mono?: boolean;
  }
  export interface SelectGroup {
    label?: string;
    options: SelectOption[];
  }
</script>

<script lang="ts">
  /**
   * The single-value dropdown of the console (there is no native <select> anywhere — tests/no-native-select.spec.ts).
   * A thin wrapper around GroupedSelect: flat `options` or `groups`, a bindable `value`, `onselect` (every pick) and
   * `onchange` (only when the value changed). Trigger = button#id[data-testid=testid][data-value]; list =
   * `${testid}-list`; option = `${testid}-opt-${value}`. Keyboard + type-ahead like a native select.
   */
  import GroupedSelect from './GroupedSelect.svelte';

  type Props = {
    options?: SelectOption[];
    groups?: SelectGroup[];
    value?: string | null;
    placeholder?: string;
    /** text shown when the value is not one of the options */
    fallback?: string | null;
    disabled?: boolean;
    invalid?: boolean;
    size?: 'sm' | 'md';
    testid?: string;
    ariaLabel?: string;
    id?: string;
    /** minimum list width in px (default 180, capped by the viewport) */
    minWidth?: number;
    onselect?: (value: string) => void;
    onchange?: (value: string) => void;
  };
  let { options = [], groups, value = $bindable(null), placeholder, fallback = null, disabled = false, invalid = false, size = 'md', testid, ariaLabel, id, minWidth = 180, onselect, onchange }: Props = $props();

  const gs = $derived(groups ?? [{ options }]);
  function pick(v: string) {
    const changed = v !== value;
    value = v;
    onselect?.(v);
    if (changed) onchange?.(v);
  }
</script>

<GroupedSelect groups={gs} {value} {placeholder} {fallback} {disabled} {invalid} {size} {testid} {ariaLabel} {id} {minWidth} onselect={pick} />
