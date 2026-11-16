<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { locale, locales, setLocale, t } from '@/i18n'
import { useSession } from '@/stores/session'

const session = useSession()
const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await session.signIn(username.value.trim(), password.value)
    const next = typeof route.query.next === 'string' && route.query.next.startsWith('/') && !route.query.next.startsWith('//') ? route.query.next : '/'
    await router.replace(next)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    password.value = ''
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login">
    <!-- A playlist that is ticking along: the thing this product watches. -->
    <svg class="pulse" viewBox="0 0 1200 200" preserveAspectRatio="none" aria-hidden="true">
      <path d="M0 120 H260 l18 -70 l26 130 l22 -96 l14 36 H520 l16 -44 l22 80 l16 -36 H780 l20 -92 l28 150 l20 -84 l12 26 H1200" />
    </svg>
    <form class="login-box panel" @submit.prevent="submit">
      <div class="seg lang" role="group" :aria-label="t('common.language')">
        <button v-for="l in locales" :key="l" type="button" :lang="l" :aria-pressed="locale === l" @click="setLocale(l)">{{ l.toUpperCase() }}</button>
      </div>
      <h1>hls-monitor</h1>
      <p class="muted">{{ t('login.hlsStreamMonitoring') }}</p>
      <div v-if="error" class="notice error" role="alert">{{ error }}</div>
      <div class="field">
        <label for="login-user">{{ t('common.username') }}</label>
        <input id="login-user" v-model="username" class="input" autocomplete="username" autocapitalize="none" required autofocus />
      </div>
      <div class="field">
        <label for="login-pass">{{ t('common.password') }}</label>
        <input id="login-pass" v-model="password" class="input" type="password" autocomplete="current-password" required />
      </div>
      <button class="btn primary" type="submit" :disabled="busy">{{ busy ? t('login.signingIn') : t('login.signIn') }}</button>
    </form>
  </div>
</template>

<style scoped>
.login {
  position: relative;
  display: grid;
  place-items: center;
  min-height: 100%;
  padding: 24px 16px;
  overflow: hidden;
}

.pulse {
  position: absolute;
  left: 0;
  right: 0;
  top: 50%;
  width: 100%;
  height: 220px;
  transform: translateY(-50%);
  pointer-events: none;
}

.pulse path {
  fill: none;
  stroke: var(--accent);
  stroke-width: 1.5;
  stroke-linejoin: round;
  vector-effect: non-scaling-stroke;
  opacity: 0.28;
  stroke-dasharray: 2600;
  stroke-dashoffset: 2600;
  animation: draw 2.2s ease-out forwards;
}

@keyframes draw {
  to {
    stroke-dashoffset: 0;
  }
}

.login-box {
  position: relative;
  width: 100%;
  max-width: 340px;
  padding: 26px 24px 24px;
}

.login-box h1 {
  font-size: 22px;
}

.login-box p {
  margin: 2px 0 20px;
}

.login-box .btn {
  width: 100%;
  height: 34px;
  margin-top: 4px;
}

.lang {
  position: absolute;
  top: 26px;
  right: 24px;
}

.lang button {
  height: 24px;
  padding: 0 7px;
  font-size: 11px;
}
</style>
