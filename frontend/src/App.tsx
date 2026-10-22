import { useEffect, useState } from 'react'

export default function App() {
  const [status, setStatus] = useState('checking...')

  useEffect(() => {
    fetch('/api/health')
      .then((res) => res.text())
      .then(setStatus)
      .catch(() => setStatus('backend unreachable'))
  }, [])

  return (
    <div className="min-h-screen flex items-center justify-center bg-slate-100">
      <p className="text-2xl font-semibold text-slate-800">Backend: {status}</p>
    </div>
  )
}