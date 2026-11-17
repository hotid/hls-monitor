// Checks the interface texts (src/locales/<language>/<namespace>.json):
//   - every key used in the source exists;
//   - every language has the same keys, with the same {parameters};
//   - plural texts have as many forms as the language needs;
//   - no Russian text is written in the source instead of a key.
// Exits with an error when it finds any; unused keys are only listed.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = new URL('../src/', import.meta.url).pathname
const localesDir = join(root, 'locales')

let failed = false
const fail = (msg) => {
  console.error(msg)
  failed = true
}

/** language -> Map("namespace.key" -> text) */
const languages = new Map()
for (const code of readdirSync(localesDir).filter((d) => statSync(join(localesDir, d)).isDirectory())) {
  const texts = new Map()
  for (const f of readdirSync(join(localesDir, code)).filter((f) => f.endsWith('.json'))) {
    for (const [k, v] of Object.entries(JSON.parse(readFileSync(join(localesDir, code, f), 'utf8')))) {
      if (typeof v !== 'string') fail(`${code}/${f}: ${k} must be a text`)
      else texts.set(f.replace(/\.json$/, '') + '.' + k, v)
    }
  }
  languages.set(code, texts)
}

const all = new Set([...languages.values()].flatMap((texts) => [...texts.keys()]))
// A parameter repeated in every form of a plural text is one parameter:
// languages differ in the number of forms.
const params = (text) => [...new Set([...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]))].sort().join(',')
// Forms of a plural text are separated by " | ".
const forms = (text) => text.split(' | ').length

for (const key of [...all].sort()) {
  const have = [...languages].filter(([, texts]) => texts.has(key))
  for (const [code, texts] of languages) if (!texts.has(key)) fail(`${code}: no text for ${key}`)
  const first = have[0]
  for (const [code, texts] of have) {
    if (params(texts.get(key)) !== params(first[1].get(key))) fail(`${code}: ${key} has parameters {${params(texts.get(key))}}, but ${first[0]} has {${params(first[1].get(key))}}`)
  }
  if (!have.some(([, texts]) => forms(texts.get(key)) > 1)) continue
  for (const [code, texts] of have) {
    // One form per plural category of the language, in the order Intl lists
    // them; the last category ("other") may be left out when it is not the
    // only one besides "one".
    const want = new Intl.PluralRules(code).resolvedOptions().pluralCategories
    const n = forms(texts.get(key))
    if (n !== want.length && !(want.length > 2 && n === want.length - 1)) fail(`${code}: ${key} needs the forms "${want.join(' | ')}", got ${JSON.stringify(texts.get(key))}`)
  }
}

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name !== 'locales') walk(p, out)
    } else if (/\.(ts|vue)$/.test(name)) out.push(p)
  }
  return out
}

const used = new Set()
for (const file of walk(root)) {
  const src = readFileSync(file, 'utf8')
  const rel = relative(root, file)
  const code = src
    .replace(/<!--[\s\S]*?-->/g, (c) => c.replace(/[^\n]/g, ''))
    .replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, ''))
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
  for (const m of code.matchAll(/(?<![\w.])\$?t\(\s*(['"`])([^'"`$]*)\1/g)) {
    used.add(m[2])
    if (!all.has(m[2])) fail(`${rel}: unknown key ${JSON.stringify(m[2])}`)
  }
  code.split('\n').forEach((line, i) => {
    if (/[А-Яа-яЁё]/.test(line)) fail(`${rel}:${i + 1}: text in the source instead of a key: ${line.trim().slice(0, 100)}`)
  })
}

for (const key of [...all].sort()) if (!used.has(key)) console.warn(`unused: ${key}`)
if (failed) process.exit(1)
console.log(`i18n: ${all.size} texts in ${[...languages.keys()].sort().join(', ')}`)
