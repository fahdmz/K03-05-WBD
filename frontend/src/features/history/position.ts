import type { HistoryEntry } from './types'

export type MigrationPosition = {
    migrationId: number
    title: string
    version: number
    applied: boolean
}

export function computePositions(entries: HistoryEntry[]) {
    const latest = new Map<number, MigrationPosition>()
    for (const entry of entries) {
        latest.set(entry.migrationId, {
            migrationId: entry.migrationId,
            title: entry.title,
            version: entry.version,
            applied: entry.action !== 'rollback',
        })
    }

    const migrations = [...latest.values()]
    const top = migrations.filter((m) => m.applied).reduce((max, m) => Math.max(max, m.version), 0)
    const rollbackTarget = migrations.find((m) => m.applied && m.version === top) ?? null
    const forwardTarget =
        migrations.filter((m) => !m.applied && m.version > top).sort((a, b) => a.version - b.version)[0] ?? null

    return { top, rollbackTarget, forwardTarget }
}