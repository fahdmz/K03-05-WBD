import { useEffect, useState } from 'react'
import { api, ApiError } from '../../lib/api'
import MigrationWorkspace from './MigrationWorkspace'

type DatabaseSummary = {
    id: number
    name: string
    level: 'view' | 'edit'
}

type SchemaSummary = { name: string }
type TableSummary = { name: string }

type Column = {
    name: string
    dataType: string
    nullable: boolean
    default: string | null
    position: number
}

type Constraint = {
    name: string
    type: string
    definition: string
}

type Index = {
    name: string
    definition: string
}

type TableDetails = {
    schema: string
    name: string
    columns: Column[]
    constraints: Constraint[]
    indexes: Index[]
}

type TableRows = {
    columns: string[]
    rows: Array<Record<string, unknown>>
    page: number
    pageSize: number
    total: number
}

type TableTab = 'structure' | 'data'
type DatabaseTab = 'explorer' | 'migrations'

function errorMessage(error: unknown) {
    if (error instanceof ApiError) return error.message
    return 'Tidak dapat terhubung ke server. Coba lagi.'
}

function displayValue(value: unknown) {
    if (value === null || value === undefined) return 'NULL'
    if (typeof value === 'object') return JSON.stringify(value)
    return String(value)
}

function EmptyState({ children }: { children: string }) {
    return <div className="rounded-xl border border-dashed border-slate-300 bg-slate-50 px-5 py-8 text-center text-sm text-slate-500">{children}</div>
}

export default function DatabasesPage() {
    const [databases, setDatabases] = useState<DatabaseSummary[]>([])
    const [selectedDatabase, setSelectedDatabase] = useState<DatabaseSummary | null>(null)
    const [schemas, setSchemas] = useState<SchemaSummary[]>([])
    const [selectedSchema, setSelectedSchema] = useState('')
    const [tables, setTables] = useState<TableSummary[]>([])
    const [selectedTable, setSelectedTable] = useState('')
    const [details, setDetails] = useState<TableDetails | null>(null)
    const [tableRows, setTableRows] = useState<TableRows | null>(null)
    const [activeTab, setActiveTab] = useState<TableTab>('structure')
    const [databaseTab, setDatabaseTab] = useState<DatabaseTab>('explorer')
    const [page, setPage] = useState(1)
    const [loading, setLoading] = useState('databases')
    const [error, setError] = useState('')

    async function loadDatabases() {
        setLoading('databases')
        setError('')
        try {
            const result = await api<DatabaseSummary[]>('/databases')
            setDatabases(result)
        } catch (cause) {
            setError(errorMessage(cause))
        } finally {
            setLoading('')
        }
    }

    useEffect(() => {
        void loadDatabases()
    }, [])

    async function openDatabase(database: DatabaseSummary) {
        setSelectedDatabase(database)
        setDatabaseTab('explorer')
        setSelectedSchema('')
        setTables([])
        setSelectedTable('')
        setDetails(null)
        setTableRows(null)
        setLoading('schemas')
        setError('')
        try {
            const result = await api<SchemaSummary[]>(`/databases/${database.id}/schemas`)
            setSchemas(result)
            if (result.length > 0) await openSchema(database, result[0].name)
        } catch (cause) {
            setError(errorMessage(cause))
        } finally {
            setLoading('')
        }
    }

    async function openSchema(database: DatabaseSummary, schema: string) {
        setSelectedSchema(schema)
        setSelectedTable('')
        setDetails(null)
        setTableRows(null)
        setLoading('tables')
        setError('')
        try {
            const result = await api<TableSummary[]>(`/databases/${database.id}/schemas/${encodeURIComponent(schema)}/tables`)
            setTables(result)
        } catch (cause) {
            setError(errorMessage(cause))
        } finally {
            setLoading('')
        }
    }

    async function loadTable(database: DatabaseSummary, schema: string, table: string, targetPage = 1) {
        setSelectedTable(table)
        setActiveTab('structure')
        setPage(targetPage)
        setLoading('table')
        setError('')
        const prefix = `/databases/${database.id}/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}`
        try {
            const [tableDetails, rows] = await Promise.all([
                api<TableDetails>(prefix),
                api<TableRows>(`${prefix}/rows?page=${targetPage}&pageSize=20`),
            ])
            setDetails(tableDetails)
            setTableRows(rows)
        } catch (cause) {
            setDetails(null)
            setTableRows(null)
            setError(errorMessage(cause))
        } finally {
            setLoading('')
        }
    }

    function closeDatabase() {
        setSelectedDatabase(null)
        setSchemas([])
        setSelectedSchema('')
        setTables([])
        setSelectedTable('')
        setDetails(null)
        setTableRows(null)
        setError('')
        setLoading('')
    }

    const canGoBack = page > 1
    const canGoForward = tableRows !== null && page * tableRows.pageSize < tableRows.total

    return (
        <div className="space-y-6">
            <div className="flex flex-wrap items-end justify-between gap-4">
                <div>
                    <p className="text-sm font-medium uppercase tracking-[0.18em] text-cyan-700">Workspace</p>
                    <h1 className="mt-2 text-3xl font-semibold tracking-tight text-slate-950">
                        {selectedDatabase ? selectedDatabase.name : 'Database'}
                    </h1>
                    <p className="mt-2 text-sm text-slate-500">
                        {selectedDatabase
                            ? 'Jelajahi schema, struktur tabel, dan isi data.'
                            : 'Pilih database yang tersedia untuk mulai menjelajah.'}
                    </p>
                </div>
                {selectedDatabase ? (
                    <button onClick={closeDatabase} className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50">
                        ← Semua database
                    </button>
                ) : (
                    <button onClick={() => void loadDatabases()} disabled={loading === 'databases'} className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50">
                        Muat ulang
                    </button>
                )}
            </div>

            {error && (
                <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
                    <span>{error}</span>
                    <button
                        onClick={() => {
                            if (!selectedDatabase) {
                                void loadDatabases()
                            } else if (selectedTable && selectedSchema) {
                                void loadTable(selectedDatabase, selectedSchema, selectedTable, page)
                            } else if (selectedSchema) {
                                void openSchema(selectedDatabase, selectedSchema)
                            } else {
                                void openDatabase(selectedDatabase)
                            }
                        }}
                        className="font-semibold underline underline-offset-2"
                    >
                        Coba lagi
                    </button>
                </div>
            )}

            {!selectedDatabase && (
                loading === 'databases' ? (
                    <EmptyState>Memuat daftar database…</EmptyState>
                ) : databases.length === 0 ? (
                    <EmptyState>Belum ada database yang bisa kamu akses.</EmptyState>
                ) : (
                    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                        {databases.map((database) => (
                            <button
                                key={database.id}
                                onClick={() => void openDatabase(database)}
                                className="group rounded-2xl border border-slate-200 bg-white p-5 text-left shadow-sm transition hover:-translate-y-0.5 hover:border-cyan-300 hover:shadow-md"
                            >
                                <div className="flex items-start justify-between gap-3">
                                    <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-cyan-50 text-lg font-semibold text-cyan-800">DB</div>
                                    <span className={`rounded-full px-2.5 py-1 text-xs font-semibold ${database.level === 'edit' ? 'bg-emerald-50 text-emerald-700' : 'bg-slate-100 text-slate-600'}`}>
                                        {database.level === 'edit' ? 'View + Edit' : 'View only'}
                                    </span>
                                </div>
                                <h2 className="mt-5 text-lg font-semibold text-slate-900 group-hover:text-cyan-800">{database.name}</h2>
                                <p className="mt-1 text-sm text-slate-500">Buka schema dan tabel database</p>
                                <span className="mt-5 inline-flex text-sm font-semibold text-cyan-800">Jelajahi <span className="ml-1 transition group-hover:translate-x-1">→</span></span>
                            </button>
                        ))}
                    </div>
                )
            )}

            {selectedDatabase && (
                <div className="space-y-4">
                    <div className="flex gap-1 border-b border-slate-200">
                        <button onClick={() => setDatabaseTab('explorer')} className={`border-b-2 px-4 py-3 text-sm font-semibold ${databaseTab === 'explorer' ? 'border-cyan-700 text-cyan-800' : 'border-transparent text-slate-500 hover:text-slate-800'}`}>Database explorer</button>
                        <button onClick={() => setDatabaseTab('migrations')} className={`border-b-2 px-4 py-3 text-sm font-semibold ${databaseTab === 'migrations' ? 'border-cyan-700 text-cyan-800' : 'border-transparent text-slate-500 hover:text-slate-800'}`}>Migration</button>
                    </div>
                    {databaseTab === 'migrations' ? <MigrationWorkspace database={selectedDatabase} /> : (
                    <div className="grid gap-5 xl:grid-cols-[260px_minmax(0,1fr)]">
                    <aside className="space-y-4 rounded-2xl border border-slate-200 bg-white p-4 shadow-sm">
                        <div>
                            <h2 className="text-sm font-semibold uppercase tracking-wide text-slate-500">Schema</h2>
                            <div className="mt-2 space-y-1">
                                {schemas.map((schema) => (
                                    <button
                                        key={schema.name}
                                        onClick={() => void openSchema(selectedDatabase, schema.name)}
                                        className={`flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-sm ${selectedSchema === schema.name ? 'bg-cyan-50 font-semibold text-cyan-900' : 'text-slate-700 hover:bg-slate-50'}`}
                                    >
                                        <span>{schema.name}</span><span className="text-xs text-slate-400">⌄</span>
                                    </button>
                                ))}
                                {loading === 'schemas' && <p className="px-3 py-2 text-sm text-slate-400">Memuat schema…</p>}
                                {loading !== 'schemas' && schemas.length === 0 && <p className="px-3 py-2 text-sm text-slate-400">Tidak ada schema.</p>}
                            </div>
                        </div>
                        <div className="border-t border-slate-100 pt-4">
                            <div className="flex items-center justify-between">
                                <h2 className="text-sm font-semibold uppercase tracking-wide text-slate-500">Tabel</h2>
                                {selectedSchema && <span className="text-xs text-slate-400">{tables.length}</span>}
                            </div>
                            <div className="mt-2 space-y-1">
                                {tables.map((table) => (
                                    <button
                                        key={table.name}
                                        onClick={() => void loadTable(selectedDatabase, selectedSchema, table.name)}
                                        className={`flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm ${selectedTable === table.name ? 'bg-slate-900 font-medium text-white' : 'text-slate-700 hover:bg-slate-50'}`}
                                    >
                                        <span className="text-xs opacity-60">▦</span>{table.name}
                                    </button>
                                ))}
                                {loading === 'tables' && <p className="px-3 py-2 text-sm text-slate-400">Memuat tabel…</p>}
                                {loading !== 'tables' && selectedSchema && tables.length === 0 && <p className="px-3 py-2 text-sm text-slate-400">Belum ada tabel.</p>}
                                {!selectedSchema && loading !== 'schemas' && <p className="px-3 py-2 text-sm text-slate-400">Pilih schema.</p>}
                            </div>
                        </div>
                    </aside>

                    <section className="min-w-0 rounded-2xl border border-slate-200 bg-white shadow-sm">
                        {!selectedTable ? (
                            <div className="p-6">
                                <EmptyState>{loading === 'schemas' || loading === 'tables' ? 'Memuat struktur database…' : 'Pilih tabel dari panel kiri untuk melihat struktur dan datanya.'}</EmptyState>
                            </div>
                        ) : (
                            <>
                                <div className="flex flex-wrap items-start justify-between gap-4 border-b border-slate-200 px-5 py-4">
                                    <div>
                                        <p className="text-xs font-medium uppercase tracking-wide text-slate-400">{selectedSchema}</p>
                                        <h2 className="mt-1 text-xl font-semibold text-slate-900">{selectedTable}</h2>
                                    </div>
                                    <span className="rounded-full bg-slate-100 px-3 py-1 text-xs font-semibold text-slate-600">{details?.columns.length ?? '…'} kolom</span>
                                </div>
                                <div className="flex gap-1 border-b border-slate-200 px-5">
                                    <button onClick={() => setActiveTab('structure')} className={`border-b-2 px-3 py-3 text-sm font-medium ${activeTab === 'structure' ? 'border-cyan-700 text-cyan-800' : 'border-transparent text-slate-500 hover:text-slate-800'}`}>Struktur</button>
                                    <button onClick={() => setActiveTab('data')} className={`border-b-2 px-3 py-3 text-sm font-medium ${activeTab === 'data' ? 'border-cyan-700 text-cyan-800' : 'border-transparent text-slate-500 hover:text-slate-800'}`}>Data</button>
                                </div>
                                {loading === 'table' ? (
                                    <div className="p-5"><EmptyState>Memuat detail tabel…</EmptyState></div>
                                ) : activeTab === 'structure' ? (
                                    <div className="space-y-6 p-5">
                                        <div>
                                            <h3 className="mb-3 text-sm font-semibold text-slate-800">Kolom</h3>
                                            <div className="overflow-x-auto rounded-xl border border-slate-200">
                                                <table className="w-full min-w-[620px] text-left text-sm">
                                                    <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500"><tr><th className="px-4 py-3">Nama</th><th className="px-4 py-3">Tipe</th><th className="px-4 py-3">Nullable</th><th className="px-4 py-3">Default</th></tr></thead>
                                                    <tbody className="divide-y divide-slate-100">
                                                        {details?.columns.map((column) => <tr key={column.position}><td className="px-4 py-3 font-medium text-slate-900">{column.name}</td><td className="px-4 py-3 font-mono text-xs text-slate-600">{column.dataType}</td><td className="px-4 py-3 text-slate-600">{column.nullable ? 'Ya' : 'Tidak'}</td><td className="max-w-64 truncate px-4 py-3 font-mono text-xs text-slate-500" title={column.default ?? undefined}>{column.default ?? '—'}</td></tr>)}
                                                        {details?.columns.length === 0 && <tr><td colSpan={4} className="px-4 py-6 text-center text-slate-400">Tidak ada kolom.</td></tr>}
                                                    </tbody>
                                                </table>
                                            </div>
                                        </div>
                                        <div className="grid gap-6 lg:grid-cols-2">
                                            <div>
                                                <h3 className="mb-3 text-sm font-semibold text-slate-800">Constraints</h3>
                                                <div className="space-y-2">
                                                    {details?.constraints.map((constraint) => <div key={constraint.name} className="rounded-xl border border-slate-200 p-3"><div className="flex flex-wrap items-center justify-between gap-2"><span className="font-mono text-sm font-medium text-slate-800">{constraint.name}</span><span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">{constraint.type}</span></div><p className="mt-2 break-words font-mono text-xs text-slate-500">{constraint.definition}</p></div>)}
                                                    {details?.constraints.length === 0 && <p className="text-sm text-slate-400">Tidak ada constraint.</p>}
                                                </div>
                                            </div>
                                            <div>
                                                <h3 className="mb-3 text-sm font-semibold text-slate-800">Index</h3>
                                                <div className="space-y-2">
                                                    {details?.indexes.map((index) => <div key={index.name} className="rounded-xl border border-slate-200 p-3"><p className="font-mono text-sm font-medium text-slate-800">{index.name}</p><p className="mt-2 break-words font-mono text-xs text-slate-500">{index.definition}</p></div>)}
                                                    {details?.indexes.length === 0 && <p className="text-sm text-slate-400">Tidak ada index.</p>}
                                                </div>
                                            </div>
                                        </div>
                                    </div>
                                ) : (
                                    <div className="p-5">
                                        {tableRows && (
                                            <>
                                                <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                                                    <p className="text-sm text-slate-500">{tableRows.total.toLocaleString()} baris · halaman {page}</p>
                                                    <div className="flex gap-2">
                                                        <button disabled={!canGoBack} onClick={() => void loadTable(selectedDatabase, selectedSchema, selectedTable, page - 1)} className="rounded-lg border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-40">Sebelumnya</button>
                                                        <button disabled={!canGoForward} onClick={() => void loadTable(selectedDatabase, selectedSchema, selectedTable, page + 1)} className="rounded-lg border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-40">Berikutnya</button>
                                                    </div>
                                                </div>
                                                {tableRows.columns.length === 0 ? <EmptyState>Tabel ini belum memiliki kolom atau data.</EmptyState> : (
                                                    <div className="overflow-x-auto rounded-xl border border-slate-200">
                                                        <table className="w-full text-left text-sm">
                                                            <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500"><tr>{tableRows.columns.map((column) => <th key={column} className="whitespace-nowrap px-4 py-3">{column}</th>)}</tr></thead>
                                                            <tbody className="divide-y divide-slate-100">
                                                                {tableRows.rows.map((row, rowIndex) => <tr key={`${page}-${rowIndex}`} className="align-top">{tableRows.columns.map((column) => <td key={column} className="max-w-80 whitespace-pre-wrap break-words px-4 py-3 font-mono text-xs text-slate-700">{displayValue(row[column])}</td>)}</tr>)}
                                                                {tableRows.rows.length === 0 && <tr><td colSpan={tableRows.columns.length} className="px-4 py-8 text-center text-slate-400">Tidak ada data di halaman ini.</td></tr>}
                                                            </tbody>
                                                        </table>
                                                    </div>
                                                )}
                                            </>
                                        )}
                                    </div>
                                )}
                            </>
                        )}
                    </section>
                    </div>
                    )}
                </div>
            )}
        </div>
    )
}
