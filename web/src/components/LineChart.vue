<script lang="ts">
export interface Series {
  name: string
  color: string
  values: number[]
}
</script>

<script setup lang="ts">
// LineChart draws one or two series as an SVG line. It is deliberately tiny:
// the platform only needs a readable trend, not an interactive chart library,
// and keeping it local avoids shipping another dependency to the browser.
import { computed } from 'vue'

const props = defineProps<{
  labels: string[]
  series: Series[]
  format: (value: number) => string
  height?: number
}>()

const viewW = 720
const viewH = 220
const padLeft = 58
const padRight = 12
const padTop = 12
const padBottom = 26

const height = computed(() => props.height ?? viewH)

const points = computed(() => {
  const count = Math.max(...props.series.map((s) => s.values.length), 0)
  const maxRaw = Math.max(1, ...props.series.flatMap((s) => s.values))
  // Round the top up so the axis carries a friendly number.
  const max = niceMax(maxRaw)
  const innerW = viewW - padLeft - padRight
  const innerH = height.value - padTop - padBottom
  return props.series.map((s) => {
    const path = s.values
      .map((v, i) => {
        const x = padLeft + (count <= 1 ? innerW / 2 : (i / (count - 1)) * innerW)
        const y = padTop + innerH - (Math.max(0, v) / max) * innerH
        return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
      })
      .join(' ')
    return { ...s, path }
  })
})

const maxLabel = computed(() => niceMax(Math.max(1, ...props.series.flatMap((s) => s.values))))

const yTicks = computed(() => {
  const max = maxLabel.value
  return [0, 0.25, 0.5, 0.75, 1].map((frac) => {
    const y = padTop + (height.value - padTop - padBottom) * (1 - frac)
    return { y, label: props.format(max * frac) }
  })
})

const xLabels = computed(() => {
  const labels = props.labels
  if (!labels.length) return []
  const innerW = viewW - padLeft - padRight
  const picks = labels.length <= 2 ? [0, labels.length - 1] : [0, Math.floor((labels.length - 1) / 2), labels.length - 1]
  return [...new Set(picks)].map((i) => ({
    x: padLeft + (labels.length <= 1 ? 0 : (i / (labels.length - 1)) * innerW),
    text: labels[i],
    anchor: i === 0 ? 'start' : i === labels.length - 1 ? 'end' : 'middle',
  }))
})

function niceMax(v: number): number {
  const exp = Math.pow(10, Math.floor(Math.log10(v)))
  const base = v / exp
  const step = base <= 1 ? 1 : base <= 2 ? 2 : base <= 5 ? 5 : 10
  return step * exp
}
</script>

<template>
  <div>
    <svg :viewBox="`0 0 ${viewW} ${height}`" class="chart" preserveAspectRatio="none" role="img">
      <line
        v-for="tick in yTicks"
        :key="'g' + tick.y"
        :x1="padLeft"
        :x2="viewW - padRight"
        :y1="tick.y"
        :y2="tick.y"
        stroke="var(--border-soft)"
        stroke-width="1"
      />
      <text v-for="tick in yTicks" :key="'t' + tick.y" :x="padLeft - 8" :y="tick.y + 4" text-anchor="end" class="axis">
        {{ tick.label }}
      </text>
      <path v-for="line in points" :key="line.name" :d="line.path" fill="none" :stroke="line.color" stroke-width="2" />
      <text
        v-for="label in xLabels"
        :key="'x' + label.x"
        :x="label.x"
        :y="height - 6"
        :text-anchor="label.anchor"
        class="axis"
      >
        {{ label.text }}
      </text>
    </svg>
    <div class="row" style="gap: 14px; margin-top: 6px">
      <span v-for="line in series" :key="'l' + line.name" class="small muted row" style="gap: 6px">
        <span :style="{ display: 'inline-block', width: '10px', height: '10px', borderRadius: '3px', background: line.color }" />
        {{ line.name }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.chart {
  width: 100%;
  height: auto;
  display: block;
}
.axis {
  fill: var(--muted-2);
  font-size: 11px;
  font-family: var(--font-mono, ui-monospace, monospace);
}
</style>
