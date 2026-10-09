<script setup lang="ts">
import { ref } from 'vue'
import { login } from '../api'
import { initHosts, state } from '../state'

const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await login(password.value)
    password.value = ''
    Object.assign(state, { loginRequired: false, error: '' })
    await initHosts()
  } catch (e: any) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen items-center justify-center px-4">
    <form class="w-full max-w-sm space-y-4" @submit.prevent="submit">
      <div class="mb-8 text-center">
        <img src="/llamira-icon-64.png" alt="" class="mx-auto h-16 w-16" width="64" height="64" />
        <h1 class="mt-4 text-2xl font-semibold">Llamira</h1>
        <p class="muted mt-1 text-sm">Your local AI model control center.</p>
        <p class="muted text-sm">Discover. Configure. Run.</p>
      </div>
      <input
        v-model="password"
        type="password"
        class="input !px-4 !py-2.5"
        placeholder="Enter your password"
        aria-label="Password"
        autocomplete="current-password"
        autofocus
        required
      />
      <div v-if="error" class="rounded-lg border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-800 dark:border-red-900 dark:bg-red-950/50 dark:text-red-200">
        {{ error }}
      </div>
      <button type="submit" class="btn btn-primary w-full !py-2.5" :disabled="busy || !password">{{ busy ? 'Signing in…' : 'Sign in' }}</button>
    </form>
  </div>
</template>
