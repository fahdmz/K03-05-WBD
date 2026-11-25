export type HistoryAction = 'apply' | 'rollback' | 'forward'

export type HistoryEntry = {
    executionId: string
    migrationId: number
    title: string
    version: number
    revisionNo: number
    action: HistoryAction
    revertsExecutionId: string | null
    executedSql: string
    mode: 'normal' | 'batch'
    batches: number | null
    rowsAffected: number | null
    requestedBy: string
    startedAt: string
    committedAt: string
}

export type CreatedRequest = {
    changeRequestId: number
    status: string
}