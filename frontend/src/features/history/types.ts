export type HistoryAction = 'apply' | 'rollback' | 'forward' | 'revert'

export type HistoryStatus = 'success' | 'failed' | 'partial'

export type HistoryEntry = {
    executionId: string
    attempt: number
    migrationId: number
    title: string
    version: number | null
    revisionNo: number
    action: HistoryAction
    status: HistoryStatus
    revertsExecutionId: string | null
    executedSql: string
    mode: 'normal' | 'batch'
    batches: number | null
    rowsAffected: number | null
    errorMessage: string | null
    sqlstate: string | null
    requestedBy: string
    startedAt: string
    finishedAt: string
}

export type CreatedRequest = {
    changeRequestId: number
    status: string
}