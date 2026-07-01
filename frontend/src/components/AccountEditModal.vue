<script setup>
import { ref } from 'vue'
import { api, jsonBody } from '../api'
import Icon from './Icon.vue'

const props = defineProps({ account: { type: Object, required: true } })
const emit = defineEmits(['close', 'saved'])

// concurrency is only adjustable for custom upstreams — others are system-fixed.
const canEditConcurrency = props.account.type === 'custom'

const weight = ref(Number(props.account.weight) || 0)
const concurrency = ref(Number(props.account.concurrency) || 1)
const status = ref('')
const isError = ref(false)
const submitting = ref(false)

async function submit() {
  submitting.value = true; status.value = ''; isError.value = false
  const payload = { weight: Number(weight.value) || 0 }
  if (canEditConcurrency) payload.concurrency = Math.max(1, Number(concurrency.value) || 1)
  try {
    const r = await api(`/tokens/${props.account.pool}/${props.account.id}`, jsonBody('PATCH', payload))
    if (r.ok) {
      status.value = '✓ 已保存'; emit('saved', payload)
      setTimeout(() => emit('close'), 600)
    } else {
      status.value = r.data?.detail || '保存失败'; isError.value = true
    }
  } catch (e) {
    status.value = String(e); isError.value = true
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-start justify-center p-4 overflow-y-auto"
       @click.self="emit('close')">
    <div class="card !shadow-2xl my-12 w-full max-w-md">
      <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between">
        <h2 class="text-sm font-semibold">编辑账号</h2>
        <button @click="emit('close')" class="text-white/40 hover:text-white">
          <Icon name="close" class="w-5 h-5" />
        </button>
      </div>
      <div class="p-5 space-y-3">
        <div>
          <label class="lbl">账户</label>
          <input :value="account.email || account.id" disabled class="field font-mono" />
        </div>
        <div>
          <label class="lbl">类型</label>
          <input :value="account.type" disabled class="field" />
        </div>
        <div>
          <label class="lbl">权重 <span class="text-white/35">(高的优先)</span></label>
          <input v-model.number="weight" type="number" class="field" placeholder="0" />
        </div>
        <div>
          <label class="lbl">并发数 <span class="text-white/35">{{ canEditConcurrency ? '(单账号)' : '(系统固定,不可调整)' }}</span></label>
          <input v-if="canEditConcurrency" v-model.number="concurrency" type="number" min="1" class="field" placeholder="1" />
          <input v-else :value="account.type === 'grok' ? 10 : 1" disabled class="field" />
        </div>
        <p v-if="status" class="text-xs" :class="isError ? 'text-rose-400' : 'text-emerald-400'">{{ status }}</p>
        <div class="flex justify-end gap-2 pt-2">
          <button @click="emit('close')" class="btn-soft">取消</button>
          <button @click="submit" :disabled="submitting" class="btn-primary">{{ submitting ? '保存中…' : '保存' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* mirror the 用户管理 edit modal so the two are visually identical */
.lbl {
  display: block;
  font-size: 0.72rem;
  font-weight: 500;
  color: rgb(255 255 255 / 0.55);
  margin-bottom: 0.4rem;
}
/* disabled inputs — readable, but visually 'cool' so the admin knows they
   can't change them (matches UsersView). */
.field:disabled {
  opacity: 0.65;
  cursor: not-allowed;
  background: rgb(255 255 255 / 0.025);
}
</style>
