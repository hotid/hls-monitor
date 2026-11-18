<script setup lang="ts">
// What the stream showed and sounded like when it was last looked at, from
// every vantage point. Nothing at all where the picture is not checked.
import { ref } from 'vue'
import { api, basePath } from '@/api'
import { fmtAgo, fmtNumber } from '@/format'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useSession } from '@/stores/session'
import type { Incident, StreamPicture } from '@/types'
import MarkSlate from '@/components/MarkSlate.vue'

const props = defineProps<{ id: number; name?: string; incidents?: Incident[] }>()
const session = useSession()
const looks = ref<StreamPicture[]>([])
usePolling(async (signal) => {
  looks.value = await api.get<StreamPicture[]>(`/api/v1/streams/${props.id}/pictures`, signal)
}, 30_000)

// A clip the stream is found to play right now can be said to be a slate
// or the channel's own right here, as in the incident that tells of it.
function clipOf(p: StreamPicture): Incident | undefined {
  const going = (props.incidents ?? []).filter(
    (i) => i.active && i.agent_id === p.agent_id && (i.kind === 'picture_loop' || i.kind === 'audio_loop') && typeof i.details?.clip_signature === 'string' && i.details.clip_signature !== '',
  )
  return going.find((i) => i.kind === 'picture_loop') ?? going[0]
}
const src = (p: StreamPicture) => `${basePath}/api/v1/streams/${p.stream_id}/picture?agent=${p.agent_id}&t=${new Date(p.time).getTime()}`
const black = (p: StreamPicture) => p.has_picture && p.luma < 20 && p.dark >= 0.98
const silent = (p: StreamPicture) => p.has_audio && p.audio_db < -70
</script>

<template>
  <div v-if="looks.length" class="looks panel">
    <figure v-for="p in looks" :key="p.agent_id">
      <img v-if="p.has_picture" :src="src(p)" :alt="t('stream.pictureOfTheStream')" loading="lazy" />
      <div v-else class="none muted">{{ t('stream.noPicture') }}</div>
      <figcaption>
        <div class="muted">{{ looks.length > 1 ? p.agent_name + ' · ' : '' }}{{ t('stream.lookedAt', { ago: fmtAgo(p.time) }) }}</div>
        <div v-if="p.has_picture" :class="{ bad: black(p) }">{{ t('stream.brightness') }} <span class="mono">{{ fmtNumber(p.luma, 0) }}</span><span class="muted"> / 255</span></div>
        <div v-if="p.has_audio" :class="{ bad: silent(p) }">{{ t('stream.soundLevel') }} <span class="mono">{{ fmtNumber(p.audio_db, 0) }} {{ t('stream.dB') }}</span></div>
        <div v-else class="muted">{{ t('stream.noSound') }}</div>
        <MarkSlate v-if="session.isAdmin && clipOf(p)" clip :picture="p.has_picture" :incident-id="clipOf(p)!.id" :stream-name="name" />
        <MarkSlate v-else-if="session.isAdmin && p.has_picture" picture :stream-id="p.stream_id" :agent-id="p.agent_id" />
      </figcaption>
    </figure>
  </div>
</template>

<style scoped>
.looks {
  display: flex;
  flex-wrap: wrap;
  gap: 16px 28px;
  padding: 12px 16px;
  margin-bottom: 16px;
}
figure {
  display: flex;
  align-items: center;
  gap: 14px;
  margin: 0;
}
img,
.none {
  width: 160px;
  border-radius: 4px;
  background: #000;
  display: block;
}
.none {
  height: 90px;
  display: grid;
  place-items: center;
  font-size: 12px;
}
figcaption {
  font-size: 13px;
  line-height: 1.6;
}
.bad {
  color: #ff7a85;
}
</style>
