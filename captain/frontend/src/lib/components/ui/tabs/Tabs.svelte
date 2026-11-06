<script lang="ts" generics="T extends string">
	let {
		options,
		value = $bindable(),
		class: className = ''
	}: { options: { value: T; label: string }[]; value?: T; class?: string } = $props();
</script>

<div
	role="tablist"
	class="inline-flex h-10 items-center gap-1 rounded-lg border border-border bg-muted/50 p-1 {className}"
>
	{#each options as o (o.value)}
		<button
			type="button"
			role="tab"
			aria-selected={value === o.value}
			tabindex={value === o.value ? 0 : -1}
			onclick={() => (value = o.value)}
			class="h-8 rounded-md px-3 text-sm font-medium transition-colors
				{value === o.value
				? 'bg-primary text-primary-foreground'
				: 'text-muted-foreground hover:text-foreground'}"
		>
			{o.label}
		</button>
	{/each}
</div>
