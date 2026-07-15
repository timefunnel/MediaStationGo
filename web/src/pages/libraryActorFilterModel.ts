import type { Media } from '../types'

export interface ActorFacet {
  name: string
  count: number
}

export function buildActorFacets(items: Media[]): ActorFacet[] {
  const actors = new Map<string, ActorFacet>()
  for (const media of items) {
    for (const name of parseActorCSV(media.actors)) {
      const key = name.toLocaleLowerCase()
      const current = actors.get(key)
      if (current) {
        current.count += 1
      } else {
        actors.set(key, { name, count: 1 })
      }
    }
  }
  const collator = new Intl.Collator('zh-CN', { numeric: true, sensitivity: 'base' })
  return Array.from(actors.values()).sort((left, right) => collator.compare(left.name, right.name))
}

export function mediaHasActor(media: Media, actor: string): boolean {
  const expected = actor.trim().toLocaleLowerCase()
  if (!expected) return true
  return parseActorCSV(media.actors).some((name) => name.toLocaleLowerCase() === expected)
}

function parseActorCSV(value?: string): string[] {
  if (!value) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const part of value.split(',')) {
    const name = part.trim()
    const key = name.toLocaleLowerCase()
    if (!name || seen.has(key)) continue
    seen.add(key)
    out.push(name)
  }
  return out
}
