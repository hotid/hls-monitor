<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, basePath } from '@/api'
import { fmtBytes, fmtDuration, fmtNumber, fmtTime } from '@/format'
import { locale, locales, setLocale, t } from '@/i18n'
import { usePolling } from '@/poll'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import ModalDialog from './ModalDialog.vue'

const session = useSession()
const live = useLive()
const toasts = useToasts()
const router = useRouter()

const { error } = usePolling((signal) => live.refresh(signal), 10_000)
usePolling((signal) => live.refreshSystem(signal), 30_000)
onBeforeUnmount(() => live.reset())

const counts = computed(() => live.overview?.counts ?? {})
// Less than a tenth of the disk left: the database shares it with others.
const diskLow = computed(() => {
  const st = live.system?.storage
  return !!st && st.disk_total_bytes > 0 && st.disk_free_bytes < st.disk_total_bytes / 10
})
const agentsOnline = computed(() => {
  const remote = (live.overview?.agents ?? []).filter((a) => a.kind === 'remote' && a.enabled)
  return { total: remote.length, online: remote.filter((a) => a.online).length }
})

async function signOut() {
  await session.signOut()
  await router.push({ name: 'login' })
}

const passwordOpen = ref(false)
const current = ref('')
const next = ref('')
const saving = ref(false)

function openPassword() {
  current.value = ''
  next.value = ''
  passwordOpen.value = true
}

async function savePassword() {
  saving.value = true
  try {
    await api.put('/api/v1/auth/password', { current: current.value, new: next.value })
    passwordOpen.value = false
    session.user = null
    toasts.ok(t('shell.passwordChangedSignInWithThe'))
    await router.push({ name: 'login' })
  } catch (e) {
    toasts.error(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="shell">
    <header class="topbar">
      <RouterLink class="brand" :to="{ name: 'dashboard' }">
        <svg width="18" height="18" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M1.5 9h2.6l1.6-5.2 2.6 9.4 2.1-6.4 1 2.2h3.1" fill="none" stroke="var(--accent)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        hls-monitor
      </RouterLink>
      <nav class="nav" :aria-label="t('shell.sections')">
        <RouterLink :to="{ name: 'dashboard' }" exact-active-class="router-link-active" active-class="">{{ t('shell.overview') }}</RouterLink>
        <RouterLink :to="{ name: 'streams' }">{{ t('common.streams') }}</RouterLink>
        <RouterLink :to="{ name: 'incidents' }">{{ t('common.incidents') }}<span v-if="live.openIncidents" class="count">{{ live.openIncidents }}</span></RouterLink>
        <RouterLink :to="{ name: 'agents' }">{{ t('common.agents') }}</RouterLink>
        <RouterLink v-if="session.isAdmin" :to="{ name: 'settings' }">{{ t('common.access') }}</RouterLink>
      </nav>
      <span class="spacer" />
      <div class="seg lang" role="group" :aria-label="t('common.language')">
        <button v-for="l in locales" :key="l" type="button" :lang="l" :aria-pressed="locale === l" @click="setLocale(l)">{{ l.toUpperCase() }}</button>
      </div>
      <button class="btn ghost" type="button" :title="t('shell.changePassword')" @click="openPassword">
        {{ session.user?.username }}
        <span v-if="!session.isAdmin" class="badge">{{ t('shell.viewOnly') }}</span>
      </button>
      <button class="btn ghost" type="button" @click="signOut">{{ t('shell.signOut') }}</button>
    </header>

    <main class="main">
      <slot />
    </main>

    <footer class="statusbar">
      <span v-if="error" class="item" style="color: #ff8a93"><span class="dot fail" />{{ error }}</span>
      <template v-else-if="live.overview">
        <span class="item"><span class="dot ok" />{{ t('shell.ok', { n: counts.ok ?? 0 }) }}</span>
        <span v-if="counts.warn" class="item"><span class="dot warn" />{{ t('common.withWarnings', { n: counts.warn }) }}</span>
        <span v-if="counts.fail" class="item"><span class="dot fail" />{{ t('common.failing', { n: counts.fail }) }}</span>
        <span v-if="counts.nodata" class="item"><span class="dot" />{{ t('shell.withNoData', { n: counts.nodata }) }}</span>
        <span v-if="agentsOnline.total" class="item hide-narrow">{{ t('shell.agentsOnlineOf', { online: agentsOnline.online, total: agentsOnline.total }) }}</span>
      </template>
      <span class="spacer" />
      <template v-if="live.system">
        <span
          v-if="live.system.load?.overloaded"
          class="item"
          style="color: #ff8a93"
          :title="t('shell.serverOverloadedHint', { cores: fmtNumber(live.system.load.cpu_cores, 1), limit: fmtNumber(live.system.load.cpu_limit, 1) })"
        >
          <span class="dot fail" />{{ t('shell.serverOverloaded') }}
        </span>
        <span
          v-else-if="live.system.checks?.behind"
          class="item hide-narrow"
          :title="t('shell.checksBehindHint', { lag: fmtDuration(live.system.checks.lag_max_seconds) })"
        >
          <span class="dot warn" />{{ t('shell.checksBehind', { n: live.system.checks.behind }, live.system.checks.behind) }}
        </span>
        <span v-if="live.system.results?.failed" class="item hide-narrow">
          <span class="dot fail" />{{ t('shell.resultsLost', { n: live.system.results.failed }, live.system.results.failed) }}
        </span>
        <span v-if="!live.system.metrics.enabled" class="item hide-narrow" :title="t('shell.hlsmonVMURLIsNotSet')">
          <span class="dot warn" />{{ t('shell.metricsStorageIsNotConnected') }}
        </span>
        <span v-else-if="!live.system.metrics_up" class="item hide-narrow" :title="t('shell.queuedForWriting', { size: fmtBytes(live.system.metrics.queued_bytes + (live.system.metrics.disk_bytes ?? 0)) })">
          <span class="dot fail" />{{ t('common.metricsStorageIsUnavailable') }}
        </span>
        <span
          v-if="live.system.storage"
          class="item hide-narrow"
          :style="diskLow ? 'color: #ff8a93' : ''"
          :title="t('shell.databaseSegmentJournalOfRecordsOf', { db: fmtBytes(live.system.storage.database_bytes), journal: fmtBytes(live.system.storage.journal_bytes), max: fmtBytes(live.system.storage.journal_max_bytes), rows: live.system.storage.journal_rows, free: fmtBytes(live.system.storage.disk_free_bytes), total: fmtBytes(live.system.storage.disk_total_bytes) })"
        >
          <span v-if="diskLow" class="dot fail" />{{ diskLow ? t('shell.diskAlmostFullFree', { free: fmtBytes(live.system.storage.disk_free_bytes) }) : t('shell.data', { size: fmtBytes(live.system.storage.database_bytes + live.system.storage.journal_bytes) }) }}
        </span>
        <a class="item hide-narrow" :href="basePath + '/swagger/index.html'" target="_blank" rel="noopener">API</a>
        <span class="item hide-narrow mono">v{{ live.system.version }}</span>
      </template>
      <span v-if="live.updatedAt" class="item mono" :title="t('shell.dataUpdated')">{{ fmtTime(live.updatedAt) }}</span>
    </footer>

    <ModalDialog v-if="passwordOpen" :title="t('shell.changePassword')" @close="passwordOpen = false">
      <form @submit.prevent="savePassword">
        <div class="modal-body">
          <div class="field">
            <label for="pw-current">{{ t('shell.currentPassword') }}</label>
            <input id="pw-current" v-model="current" class="input" type="password" autocomplete="current-password" required />
          </div>
          <div class="field">
            <label for="pw-new">{{ t('common.newPassword') }}</label>
            <input id="pw-new" v-model="next" class="input" type="password" autocomplete="new-password" minlength="8" required />
            <span class="help">{{ t('shell.atLeast8CharactersChangingIt') }}</span>
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn" type="button" @click="passwordOpen = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" type="submit" :disabled="saving">{{ t('shell.changePassword') }}</button>
        </div>
      </form>
    </ModalDialog>
  </div>
</template>

<style scoped>
.lang {
  flex: none;
  margin-right: 6px;
}

.lang button {
  height: 24px;
  padding: 0 7px;
  font-size: 11px;
}
</style>
