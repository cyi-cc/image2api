<script setup>
import { computed } from 'vue'
import { site } from '../site'
import Mascot from '../components/Mascot.vue'

const contact = computed(() => site.contact || {})

const powers = [
  { icon: '🎨', title: '文字生图', desc: '一句话出图，画幅与分辨率随模型可选。', tint: 'from-fuchsia-400/20 to-pink-400/10' },
  { icon: '🖼️', title: '参考图生图', desc: '上传参考换风格、改构图、做变体。', tint: 'from-violet-400/20 to-indigo-400/10' },
  { icon: '🎬', title: '文字生视频', desc: '一段描述生成短片，时长分辨率可选。', tint: 'from-sky-400/20 to-cyan-400/10' },
  { icon: '✨', title: '图生视频', desc: '给张参考图，让静态画面动起来。', tint: 'from-amber-400/20 to-orange-400/10' },
]
</script>

<template>
  <div class="space-y-16 pb-8">
    <!-- ===== hero ===== -->
    <section class="relative overflow-hidden rounded-[2rem] anime-card px-6 py-10 md:px-12 md:py-14">
      <div class="anime-stars"></div>
      <div class="relative grid md:grid-cols-[1.1fr_0.9fr] gap-8 items-center">
        <div>
          <span class="sticker">✦ 关于我们</span>
          <h1 class="mt-4 text-4xl md:text-5xl font-extrabold tracking-tight text-[color:var(--fg)]">
            嗨，我是 <span class="anime-grad-text">薇薇</span>
          </h1>
          <p class="text-[color:var(--fg-2)] mt-5 text-base md:text-lg leading-relaxed max-w-xl">
            {{ site.title }} 是一个聚合 GPT、Gemini、Firefly、Flux 等顶级模型的 AI 生图与生视频平台。
            把脑海里的画面写成一句话，交给我替你变成图像与视频。
          </p>
          <router-link to="/user" class="bubble-btn mt-7">开始创作 →</router-link>
        </div>
        <div class="grid place-items-center">
          <Mascot :size="240" />
        </div>
      </div>
    </section>

    <!-- ===== powers ===== -->
    <section>
      <div class="anime-eyebrow text-fuchsia-500 dark:text-fuchsia-300">能做什么</div>
      <h2 class="mt-3 text-2xl md:text-3xl font-extrabold tracking-tight text-[color:var(--fg)]">四种方式，把想象变成画面</h2>
      <div class="mt-7 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
        <div v-for="p in powers" :key="p.title"
             class="anime-card p-5 bg-gradient-to-br" :class="p.tint">
          <div class="text-3xl">{{ p.icon }}</div>
          <div class="mt-3 text-base font-bold text-[color:var(--fg)]">{{ p.title }}</div>
          <div class="mt-1.5 text-[13px] leading-relaxed text-[color:var(--fg-3)]">{{ p.desc }}</div>
        </div>
      </div>
    </section>

    <!-- ===== contact ===== -->
    <section>
      <div class="anime-eyebrow text-sky-500 dark:text-sky-300">联系我们</div>
      <h2 class="mt-3 text-2xl md:text-3xl font-extrabold tracking-tight text-[color:var(--fg)]">有问题？来找我们玩～</h2>
      <p class="text-[color:var(--fg-3)] mt-2 mb-6">合作、反馈，或想加入交流群，随时招呼。</p>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 max-w-3xl">
        <a v-if="contact.shop" :href="contact.shop" target="_blank" rel="noopener"
           class="group anime-card flex items-center justify-between gap-3 p-5">
          <div>
            <div class="sticker">🛍️ 商店</div>
            <div class="text-base font-semibold text-[color:var(--fg)] mt-2 group-hover:text-fuchsia-400 transition-colors">前往商店</div>
          </div>
          <span class="text-[color:var(--fg-faint)] group-hover:translate-x-1 transition-transform">→</span>
        </a>

        <a v-if="contact.email" :href="`mailto:${contact.email}`"
           class="group anime-card p-5">
          <div class="sticker">✉️ 邮箱</div>
          <div class="text-base font-semibold text-[color:var(--fg)] mt-2 break-all group-hover:text-sky-400 transition-colors">{{ contact.email }}</div>
        </a>

        <a v-if="contact.qq && contact.qq_link" :href="contact.qq_link" target="_blank" rel="noopener"
           class="group anime-card flex items-center justify-between gap-3 p-5">
          <div>
            <div class="sticker">💬 QQ</div>
            <div class="text-base font-semibold text-[color:var(--fg)] mt-2 tabular-nums break-all group-hover:text-sky-400 transition-colors">{{ contact.qq }}</div>
          </div>
          <span class="text-[color:var(--fg-faint)] group-hover:translate-x-1 transition-transform">→</span>
        </a>
        <div v-else-if="contact.qq" class="anime-card p-5">
          <div class="sticker">💬 QQ</div>
          <div class="text-base font-semibold text-[color:var(--fg)] mt-2 tabular-nums break-all">{{ contact.qq }}</div>
        </div>

        <a v-if="contact.qq_group && contact.qq_group_link" :href="contact.qq_group_link" target="_blank" rel="noopener"
           class="group anime-card flex items-center justify-between gap-3 p-5">
          <div>
            <div class="sticker">🎀 QQ 群</div>
            <div class="text-base font-semibold text-[color:var(--fg)] mt-2 tabular-nums break-all group-hover:text-emerald-400 transition-colors">{{ contact.qq_group }}</div>
          </div>
          <span class="text-[color:var(--fg-faint)] group-hover:translate-x-1 transition-transform">→</span>
        </a>
        <div v-else-if="contact.qq_group" class="anime-card p-5">
          <div class="sticker">🎀 QQ 群</div>
          <div class="text-base font-semibold text-[color:var(--fg)] mt-2 tabular-nums break-all">{{ contact.qq_group }}</div>
        </div>
      </div>

      <p v-if="!contact.shop && !contact.email && !contact.qq && !contact.qq_group"
         class="text-sm text-[color:var(--fg-3)]">管理员尚未配置联系方式。</p>
    </section>
  </div>
</template>
