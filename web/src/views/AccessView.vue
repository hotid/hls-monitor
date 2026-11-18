<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api, basePath } from '@/api'
import { fmtAgo, fmtFull } from '@/format'
import { t } from '@/i18n'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import type { ApiToken, Role, User } from '@/types'
import SlatesPanel from '@/components/SlatesPanel.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ModalDialog from '@/components/ModalDialog.vue'

const session = useSession()
const toasts = useToasts()

const users = ref<User[] | null>(null)
const tokens = ref<ApiToken[] | null>(null)
const busy = ref(false)

async function load() {
  try {
    ;[users.value, tokens.value] = await Promise.all([api.get<User[]>('/api/v1/users'), api.get<ApiToken[]>('/api/v1/tokens')])
  } catch (e) {
    toasts.error(e)
  }
}
onMounted(load)

// ── Users ────────────────────────────────────────────────────────────
const userForm = reactive({ open: false, id: 0, username: '', password: '', role: 'viewer' as Role })
const deletingUser = ref<User | null>(null)

function newUser() {
  Object.assign(userForm, { open: true, id: 0, username: '', password: '', role: 'viewer' })
}

function editUser(u: User) {
  Object.assign(userForm, { open: true, id: u.id, username: u.username, password: '', role: u.role })
}

async function saveUser() {
  busy.value = true
  try {
    if (userForm.id) {
      await api.put(`/api/v1/users/${userForm.id}`, { role: userForm.role, ...(userForm.password ? { password: userForm.password } : {}) })
      toasts.ok(t('access.userSaved'))
    } else {
      await api.post('/api/v1/users', { username: userForm.username.trim(), password: userForm.password, role: userForm.role })
      toasts.ok(t('access.userAdded'))
    }
    userForm.open = false
    await load()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

async function toggleUser(u: User) {
  try {
    await api.put(`/api/v1/users/${u.id}`, { disabled: !u.disabled })
    toasts.ok(u.disabled ? t('access.signInAllowedFor', { name: u.username }) : t('access.signInDeniedFor', { name: u.username }))
    await load()
  } catch (e) {
    toasts.error(e)
  }
}

async function removeUser() {
  const u = deletingUser.value
  if (!u) return
  busy.value = true
  try {
    await api.del(`/api/v1/users/${u.id}`)
    deletingUser.value = null
    toasts.ok(t('access.userDeleted', { name: u.username }))
    await load()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

// ── API tokens ───────────────────────────────────────────────────────
const tokenName = ref('')
const creatingToken = ref(false)
const created = ref<ApiToken | null>(null)
const deletingToken = ref<ApiToken | null>(null)

async function createToken() {
  busy.value = true
  try {
    created.value = await api.post<ApiToken>('/api/v1/tokens', { name: tokenName.value.trim() })
    creatingToken.value = false
    tokenName.value = ''
    await load()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

async function removeToken() {
  const tok = deletingToken.value
  if (!tok) return
  busy.value = true
  try {
    await api.del(`/api/v1/tokens/${tok.id}`)
    deletingToken.value = null
    toasts.ok(t('access.tokenRevoked', { name: tok.name }))
    await load()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.ok(t('common.copied'))
  } catch {
    toasts.error(t('common.couldNotCopySelectTheText'))
  }
}

const origin = window.location.origin + basePath
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h1>{{ t('common.access') }}</h1>
    </div>

    <section>
      <div class="section-title">
        <h2>{{ t('access.users') }}</h2>
        <span class="spacer" style="flex: 1" />
        <button class="btn primary" type="button" @click="newUser">{{ t('access.addUser') }}</button>
      </div>
      <div class="panel">
        <div v-if="!users" class="empty">{{ t('common.loading') }}</div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t('access.name') }}</th>
                <th>{{ t('access.role') }}</th>
                <th>{{ t('access.signIn') }}</th>
                <th class="hide-narrow">{{ t('access.created') }}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="strong">{{ u.username }} <span v-if="u.id === session.user?.id" class="badge accent">{{ t('access.you') }}</span></td>
                <td>{{ u.role === 'admin' ? t('access.administrator') : t('access.viewOnly') }}</td>
                <td>
                  <span class="badge" :class="u.disabled ? 'fail' : 'ok'">{{ u.disabled ? t('access.denied') : t('access.allowed') }}</span>
                </td>
                <td class="hide-narrow">{{ fmtFull(u.created_at) }}</td>
                <td class="actions">
                  <button class="btn small" type="button" @click="editUser(u)">{{ t('access.edit') }}</button>
                  <button class="btn small" type="button" :disabled="u.id === session.user?.id" @click="toggleUser(u)">{{ u.disabled ? t('access.allowSignIn') : t('access.denySignIn') }}</button>
                  <button class="btn small danger" type="button" :disabled="u.id === session.user?.id" @click="deletingUser = u">{{ t('common.delete') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <section class="section">
      <div class="section-title">
        <h2>{{ t('access.tokensForExternalMonitoring') }}</h2>
        <span class="spacer" style="flex: 1" />
        <button class="btn primary" type="button" @click="creatingToken = true">{{ t('access.createToken') }}</button>
      </div>
      <p class="muted lead">
        {{ t('access.aTokenGivesReadOnlyAccess') }} <span class="mono">/api/ext/v1</span>{{ t('access.discoveryOfStreamsVariantsAndAgents') }}
        {{ t('access.theRequestsAreDescribedInThe') }} <a :href="basePath + '/swagger/index.html'" target="_blank" rel="noopener">{{ t('access.apiDocumentation') }}</a>.
      </p>
      <div class="panel">
        <div v-if="!tokens" class="empty">{{ t('common.loading') }}</div>
        <div v-else-if="!tokens.length" class="empty">{{ t('access.noTokensYet') }}</div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t('common.name') }}</th>
                <th>{{ t('access.created') }}</th>
                <th>{{ t('access.lastUsed') }}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              <tr v-for="tok in tokens" :key="tok.id">
                <td class="strong">{{ tok.name }}</td>
                <td>{{ fmtFull(tok.created_at) }}</td>
                <td>{{ fmtAgo(tok.last_used_at) }}</td>
                <td class="actions"><button class="btn small danger" type="button" @click="deletingToken = tok">{{ t('access.revoke') }}</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <ModalDialog v-if="userForm.open" :title="userForm.id ? t('access.user', { name: userForm.username }) : t('access.addUser')" @close="userForm.open = false">
      <form @submit.prevent="saveUser">
        <div class="modal-body">
          <div v-if="!userForm.id" class="field">
            <label for="u-name">{{ t('common.username') }}</label>
            <input id="u-name" v-model="userForm.username" class="input" autocomplete="off" pattern="[A-Za-z0-9][A-Za-z0-9._\-]{0,62}" required />
            <span class="help">{{ t('access.latinLettersDigitsDotHyphenAnd') }}</span>
          </div>
          <div class="field">
            <label for="u-role">{{ t('access.role') }}</label>
            <select id="u-role" v-model="userForm.role" class="input" :disabled="userForm.id === session.user?.id">
              <option value="viewer">{{ t('access.viewOnly') }}</option>
              <option value="admin">{{ t('access.administrator') }}</option>
            </select>
            <span class="help">{{ t('access.anAdministratorChangesStreamsAgentsAnd') }}</span>
          </div>
          <div class="field">
            <label for="u-pass">{{ userForm.id ? t('common.newPassword') : t('common.password') }}</label>
            <input id="u-pass" v-model="userForm.password" class="input" type="password" autocomplete="new-password" minlength="8" :required="!userForm.id" />
            <span class="help">
              {{ t('access.atLeast8Characters') }}
              <template v-if="userForm.id">{{ t('access.leaveEmptyToKeepTheCurrent') }}</template>
            </span>
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn" type="button" @click="userForm.open = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" type="submit" :disabled="busy">{{ userForm.id ? t('common.save') : t('access.addUser') }}</button>
        </div>
      </form>
    </ModalDialog>

    <ModalDialog v-if="creatingToken" :title="t('access.createToken')" @close="creatingToken = false">
      <form @submit.prevent="createToken">
        <div class="modal-body">
          <div class="field">
            <label for="t-name">{{ t('common.name') }}</label>
            <input id="t-name" v-model="tokenName" class="input" placeholder="zabbix" pattern="[A-Za-z0-9][A-Za-z0-9._\-]{0,62}" required />
            <span class="help">{{ t('access.soThatYouCanTellLater') }}</span>
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn" type="button" @click="creatingToken = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" type="submit" :disabled="busy">{{ t('access.createToken') }}</button>
        </div>
      </form>
    </ModalDialog>

    <ModalDialog v-if="created" :title="t('access.tokenNamed', { name: created.name })" wide @close="created = null">
      <div class="modal-body">
        <div class="notice">{{ t('access.theTokenIsShownOnlyOnce') }}</div>
        <div class="field">
          <span class="label">{{ t('access.token') }}</span>
          <pre class="code wrap">{{ created.token }}</pre>
        </div>
        <div class="field">
          <span class="label">{{ t('access.exampleRequest') }}</span>
          <pre class="code wrap">curl -H "Authorization: Bearer {{ created.token }}" {{ origin }}/api/ext/v1/status</pre>
        </div>
      </div>
      <div class="modal-foot">
        <button class="btn" type="button" @click="copy(created.token ?? '')">{{ t('common.copyToken') }}</button>
        <span class="spacer" />
        <button class="btn primary" type="button" @click="created = null">{{ t('common.done') }}</button>
      </div>
    </ModalDialog>

    <ConfirmDialog v-if="deletingUser" :title="t('access.deleteUserQuestion')" :action="t('access.deleteUser')" :busy="busy" @close="deletingUser = null" @confirm="removeUser">
      <p>{{ t('access.userWillNoLongerBeAble', { name: deletingUser.username }) }}</p>
    </ConfirmDialog>
    <SlatesPanel />

    <ConfirmDialog v-if="deletingToken" :title="t('access.revokeTokenQuestion')" :action="t('access.revokeToken')" :busy="busy" @close="deletingToken = null" @confirm="removeToken">
      <p>{{ t('access.systemsThatUseTheTokenWill', { name: deletingToken.name }) }}</p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.lead {
  max-width: 78ch;
  margin: -2px 0 12px;
}
</style>
