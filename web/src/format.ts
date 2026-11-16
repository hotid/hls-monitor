// Formatting helpers. Numbers are shown the way an engineer reads them:
// few significant digits, units attached.

import { dateLocale, perLocale, t } from './i18n'

export function fmtBits(bps: number | undefined | null): string {
  if (bps == null || !isFinite(bps)) return '—'
  if (bps >= 1e9) return (bps / 1e9).toFixed(2) + ' ' + t('format.gbps')
  if (bps >= 1e6) return (bps / 1e6).toFixed(2) + ' ' + t('format.mbps')
  if (bps >= 1e3) return (bps / 1e3).toFixed(0) + ' ' + t('format.kbps')
  return bps.toFixed(0) + ' ' + t('format.bps')
}

export function fmtBytes(n: number | undefined | null): string {
  if (n == null || !isFinite(n)) return '—'
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + ' ' + t('format.gb')
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(2) + ' ' + t('format.mb')
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' ' + t('format.kb')
  return n.toFixed(0) + ' ' + t('format.b')
}

/** Seconds, shown as ms below one second. */
export function fmtSeconds(s: number | undefined | null): string {
  if (s == null || !isFinite(s)) return '—'
  const a = Math.abs(s)
  if (a === 0) return '0'
  if (a < 0.001) return (s * 1e6).toFixed(0) + ' ' + t('format.us')
  if (a < 1) return (s * 1000).toFixed(a < 0.01 ? 2 : 0) + ' ' + t('format.ms')
  if (a < 60) return s.toFixed(a < 10 ? 2 : 1) + ' ' + t('format.s')
  return fmtDuration(s)
}

export function fmtMs(ms: number | undefined | null): string {
  return ms == null ? '—' : fmtSeconds(ms / 1000)
}

/** A span of time in the largest two units. */
export function fmtDuration(sec: number): string {
  sec = Math.max(0, Math.round(sec))
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (d > 0) return `${d} ${t('format.d')} ${h} ${t('format.h')}`
  if (h > 0) return `${h} ${t('format.h')} ${m} ${t('format.min')}`
  if (m > 0) return `${m} ${t('format.min')} ${s} ${t('format.s')}`
  return `${s} ${t('format.s')}`
}

export function fmtNumber(v: number | undefined | null, digits = 2): string {
  if (v == null || !isFinite(v)) return '—'
  return Number.isInteger(v) ? String(v) : v.toFixed(digits)
}

const formats = perLocale(() => ({
  time: new Intl.DateTimeFormat(dateLocale(), { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
  dateTime: new Intl.DateTimeFormat(dateLocale(), { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }),
  full: new Intl.DateTimeFormat(dateLocale(), { dateStyle: 'medium', timeStyle: 'medium' }),
}))

export function fmtTime(at: string | number | Date | null | undefined): string {
  return at == null ? '—' : formats().time.format(new Date(at))
}

/** Time of day for today, date and time otherwise. */
export function fmtWhen(at: string | number | Date | null | undefined): string {
  if (at == null) return '—'
  const d = new Date(at)
  return d.toDateString() === new Date().toDateString() ? formats().time.format(d) : formats().dateTime.format(d)
}

export function fmtFull(at: string | number | Date | null | undefined): string {
  return at == null ? '—' : formats().full.format(new Date(at))
}

/** "3 min ago". */
export function fmtAgo(at: string | number | Date | null | undefined, now = Date.now()): string {
  if (at == null) return t('format.never')
  const sec = (now - new Date(at).getTime()) / 1000
  if (sec < 5) return t('format.justNow')
  return t('format.ago', { span: fmtDuration(sec) })
}
