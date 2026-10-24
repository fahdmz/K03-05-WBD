import AdminPage from './features/admin/AdminPage'

export default function App() {
  return (
    <div className="admin-shell">
      <header className="admin-shell-header">
        <div>
          <strong>DB Platform</strong>
          <span>Administrasi</span>
        </div>
      </header>

      <main>
        <AdminPage />
      </main>
    </div>
  )
}