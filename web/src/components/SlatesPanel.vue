<script setup lang="ts">
// The pictures marked as slates, and the way to forget one.
import { ref } from 'vue'
import { api, basePath } from '@/api'
import { fmtWhen } from '@/format'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useToasts } from '@/stores/toasts'
import type { Slate } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const toasts = useToasts()
const slates = ref<Slate[]>([])
const { refresh } = usePolling(async (signal) => {
  slates.value = await api.get<Slate[]>('/api/v1/slates', signal)
}, 60_000)

// What is about to be deleted: a whole slate, or one picture of it.
const deleting = ref<{ slate: Slate; frame?: number } | null>(null)
const busy = ref(false)
async function remove() {
  const d = deleting.value
  if (!d) return
  busy.value = true
  try {
    await api.del(d.frame ? `/api/v1/slates/frames/${d.frame}` : `/api/v1/slates/${d.slate.id}`)
    deleting.value = null
    await refresh()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="slates">
    <div class="section-title">
      <h2>{{ t('access.slates') }}</h2>
    </div>
    <p class="muted lead">{{ t('access.slatesLead') }}</p>
    <div class="panel">
      <div v-if="!slates.length" class="empty">{{ t('access.noSlates') }}</div>
      <table v-else class="table">
        <tbody>
          <tr v-for="s in slates" :key="s.id">
            <td class="strong name">{{ s.name }}<div class="muted when">{{ fmtWhen(s.created_at) }}</div></td>
            <td>
              <div class="frames">
                <figure v-for="f in s.frames" :key="f.id">
                  <img :src="`${basePath}/api/v1/slates/frames/${f.id}`" :alt="s.name" loading="lazy" />
                  <button class="drop" type="button" :title="t('access.deleteThisPicture')" :aria-label="t('access.deleteThisPicture')" @click="deleting = { slate: s, frame: f.id }">×</button>
                </figure>
              </div>
            </td>
            <td class="right"><button class="btn danger" type="button" @click="deleting = { slate: s }">{{ t('access.deleteSlate') }}</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <ConfirmDialog
      v-if="deleting" :title="deleting.frame ? t('access.deletePictureQuestion') : t('access.deleteSlateQuestion')"
      :action="t('common.delete')" :busy="busy" @close="deleting = null" @confirm="remove"
    >
      <p v-if="deleting.frame">{{ deleting.slate.frames.length > 1 ? t('access.pictureWillBeForgotten', { name: deleting.slate.name }) : t('access.lastPictureTakesTheSlate', { name: deleting.slate.name }) }}</p>
      <p v-else>{{ t('access.forgetSlate', { name: deleting.slate.name }) }}</p>
      <img v-if="deleting.frame" class="preview" :src="`${basePath}/api/v1/slates/frames/${deleting.frame}`" :alt="deleting.slate.name" />
    </ConfirmDialog>
  </section>
</template>

<style scoped>
.slates {
  margin-top: 28px;
}
.lead {
  max-width: 78ch;
  margin: -2px 0 12px;
}
.name {
  width: 260px;
  vertical-align: top;
}
.when {
  font-weight: 400;
  font-size: 12px;
}
.frames {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.frames figure {
  position: relative;
  margin: 0;
}
.frames img,
.preview {
  width: 128px;
  border-radius: 4px;
  background: #000;
  display: block;
}
.preview {
  width: 160px;
  margin-top: 10px;
}
.drop {
  position: absolute;
  top: 3px;
  right: 3px;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: rgba(10, 22, 40, 0.8);
  color: #fff;
  font-size: 14px;
  line-height: 20px;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.1s;
}
.frames figure:hover .drop,
.drop:focus-visible {
  opacity: 1;
}
.drop:hover {
  background: #e63946;
}
</style>
