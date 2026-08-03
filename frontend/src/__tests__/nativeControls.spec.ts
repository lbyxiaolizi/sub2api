import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

function collectProductionSources(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    if (entry === '__tests__') return []

    const path = resolve(dir, entry)
    const stat = statSync(path)
    if (stat.isDirectory()) return collectProductionSources(path)
    if (!/\.(ts|vue)$/.test(entry) || entry.endsWith('.d.ts')) return []
    return [path]
  })
}

const productionSources = collectProductionSources(sourceRoot)

function stripComments(source: string): string {
  return source
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1')
}

describe('native browser controls', () => {
  it('uses shared Select instead of native select tags in production Vue files', () => {
    const offenders = productionSources
      .filter((path) => path.endsWith('.vue'))
      .filter((path) => /<select(\s|>|\/)/.test(readFileSync(path, 'utf8')))

    expect(offenders).toEqual([])
  })

  it('does not call window.alert/confirm/prompt in production source', () => {
    // 只拦截显式的 window.alert/confirm/prompt 调用；局部同名函数与
    // 字符串字面量（如 i18n 文案中的 "prompt (...)"）不算浏览器对话框。
    const nativeDialogCall = /\bwindow\.(?:alert|confirm|prompt)\s*\(/
    const offenders = productionSources.filter((path) =>
      nativeDialogCall.test(stripComments(readFileSync(path, 'utf8'))),
    )

    expect(offenders).toEqual([])
  })
})
