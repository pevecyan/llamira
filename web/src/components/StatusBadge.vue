<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ status: string; failed?: boolean }>()

const cls = computed(() => {
  if (props.failed) return 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300'
  switch (props.status) {
    case 'loaded':
      return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300'
    case 'loading':
    case 'downloading':
      return 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300'
    case 'sleeping':
      return 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300'
    default:
      return 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400'
  }
})
</script>

<template>
  <span class="badge" :class="cls">
    <span
      class="h-1.5 w-1.5 rounded-full bg-current"
      :class="{ 'animate-pulse': status === 'loading' || status === 'downloading' }"
    />
    {{ failed ? 'failed' : status }}
  </span>
</template>
