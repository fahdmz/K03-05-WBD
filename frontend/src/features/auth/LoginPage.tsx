import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router-dom'
import { ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'

export default function LoginPage() {
    const { user, loading, login } = useAuth()
    const [username, setUsername] = useState('')
    const [password, setPassword] = useState('')
    const [error, setError] = useState('')
    const [submitting, setSubmitting] = useState(false)

    if (!loading && user) return <Navigate to="/" replace />

    async function handleSubmit(e: FormEvent) {
        e.preventDefault()
        setError('')
        setSubmitting(true)
        try {
            await login(username, password)
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Tidak dapat terhubung ke server')
        } finally {
            setSubmitting(false)
        }
    }

    return (
        <div className="flex min-h-screen items-center justify-center bg-slate-100 px-4">
            <form
                onSubmit={handleSubmit}
                className="w-full max-w-sm space-y-4 rounded-lg bg-white p-6 shadow"
            >
                <h1 className="text-xl font-semibold">Masuk</h1>
                {error && <p className="rounded bg-red-50 px-3 py-2 text-sm text-red-700">{error}</p>}
                <label className="block text-sm">
                    Username
                    <input
                        value={username}
                        onChange={(e) => setUsername(e.target.value)}
                        autoComplete="username"
                        required
                        className="mt-1 w-full rounded border border-slate-300 px-3 py-2"
                    />
                </label>
                <label className="block text-sm">
                    Password
                    <input
                        type="password"
                        value={password}
                        onChange={(e) => setPassword(e.target.value)}
                        autoComplete="current-password"
                        required
                        className="mt-1 w-full rounded border border-slate-300 px-3 py-2"
                    />
                </label>
                <button
                    type="submit"
                    disabled={submitting}
                    className="w-full rounded bg-slate-900 py-2 text-white hover:bg-slate-700 disabled:opacity-50"
                >
                    {submitting ? 'Memproses...' : 'Masuk'}
                </button>
            </form>
        </div>
    )
}