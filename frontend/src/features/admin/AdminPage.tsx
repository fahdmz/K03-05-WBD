import { useEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { adminApi, adminErrorMessage } from './api'
import type { Access, Database, Group, User } from './types'
import './admin.css'

type Section = 'users' | 'groups' | 'databases'

type Editor =
  | { kind: 'user'; row?: User }
  | { kind: 'group'; row?: Group }
  | { kind: 'database'; row?: Database }
  | { kind: 'members'; row: Group }
  | { kind: 'access'; row: Group }
  | {
      kind: 'delete'
      resource: Section
      id: number
      name: string
    }

type Data = {
  users: User[]
  groups: Group[]
  databases: Database[]
}

const sections: { id: Section; label: string; description: string }[] = [
  {
    id: 'users',
    label: 'User',
    description: 'Kelola akun, status aktif, dan hak admin platform.',
  },
  {
    id: 'groups',
    label: 'Group',
    description: 'Atur anggota dan izin akses setiap group ke database.',
  },
  {
    id: 'databases',
    label: 'Koneksi Database',
    description: 'Kelola kredensial dan uji koneksi PostgreSQL.',
  },
]

function Table({
  headings,
  children,
  empty,
}: {
  headings: string[]
  children: ReactNode
  empty: boolean
}) {
  return (
    <div className="admin-table-scroll">
      <table>
        <thead>
          <tr>
            {headings.map((heading) => (
              <th key={heading} scope="col">{heading}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {empty ? (
            <tr>
              <td colSpan={headings.length} className="admin-empty">
                Belum ada data yang sesuai.
              </td>
            </tr>
          ) : children}
        </tbody>
      </table>
    </div>
  )
}

function Field({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <label className="admin-field">
      <span>{label}</span>
      {children}
    </label>
  )
}

function editorTitle(editor: Editor): string {
  switch (editor.kind) {
    case 'user':
      return editor.row ? 'Edit user' : 'Tambah user'
    case 'group':
      return editor.row ? 'Edit group' : 'Tambah group'
    case 'database':
      return editor.row ? 'Edit koneksi database' : 'Tambah koneksi database'
    case 'members':
      return `Anggota — ${editor.row.name}`
    case 'access':
      return `Akses database — ${editor.row.name}`
    case 'delete':
      return `Hapus ${editor.name}?`
  }
}

type EditorDialogProps = {
  editor: Editor
  data: Data
  currentUserId?: number
  onClose: () => void
  onSaved: (message: string) => void
}

function EditorDialog({
  editor,
  data,
  currentUserId,
  onClose,
  onSaved,
}: EditorDialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const formRef = useRef<HTMLFormElement>(null)
  const operationPending = useRef(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState('')

  useEffect(() => {
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])

  function databaseBody(form: FormData) {
    return {
      name: String(form.get('name') ?? '').trim(),
      host: String(form.get('host') ?? '').trim(),
      port: Number(form.get('port')),
      dbname: String(form.get('dbname') ?? ''),
      username: String(form.get('username') ?? ''),
      sslmode: String(form.get('sslmode') ?? 'disable'),
    }
  }

  async function testConnection() {
    const form = formRef.current
    if (!form || operationPending.current || !form.reportValidity()) return

    const values = new FormData(form)
    const password = String(values.get('password') ?? '')

    if (editor.kind === 'database' && editor.row && password === '') {
      setError(
        'Untuk test connection, masukkan password koneksi. ' +
        'Password tersimpan tidak dikirim kembali oleh server.',
      )
      return
    }

    operationPending.current = true
    setBusy(true)
    setError('')
    setTestResult('')

    try {
      const { name: _name, ...config } = databaseBody(values)
      await adminApi('/admin/databases/test', 'POST', {
        ...config,
        password,
      })
      setTestResult('Koneksi berhasil.')
    } catch (err) {
      setError(adminErrorMessage(err))
    } finally {
      operationPending.current = false
      setBusy(false)
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (operationPending.current) return

    const values = new FormData(event.currentTarget)
    operationPending.current = true
    setBusy(true)
    setError('')
    setTestResult('')

    try {
      switch (editor.kind) {
        case 'user': {
          const password = String(values.get('password') ?? '')
          if (password && new TextEncoder().encode(password).length > 72) {
            setError('Password maksimal 72 byte.')
            return
          }

          const body: Record<string, unknown> = {
            username: String(values.get('username') ?? '').trim(),
          }

          if (!editor.row) {
            body.password = password
            body.isAdmin = values.get('isAdmin') === 'on'
          } else {
            if (password !== '') body.password = password

            const ownAccount = editor.row.id === currentUserId
            if (!ownAccount) {
              const isAdmin = values.get('isAdmin') === 'on'
              const isActive = values.get('isActive') === 'on'

              // Avoid revoking sessions when these fields did not change.
              if (isAdmin !== editor.row.isAdmin) body.isAdmin = isAdmin
              if (isActive !== editor.row.isActive) body.isActive = isActive
            }
          }

          await adminApi(
            editor.row
              ? `/admin/users/${editor.row.id}`
              : '/admin/users',
            editor.row ? 'PATCH' : 'POST',
            body,
          )
          break
        }

        case 'group':
          await adminApi(
            editor.row
              ? `/admin/groups/${editor.row.id}`
              : '/admin/groups',
            editor.row ? 'PATCH' : 'POST',
            {
              name: String(values.get('name') ?? '').trim(),
              description: String(values.get('description') ?? ''),
            },
          )
          break

        case 'members':
          await adminApi(
            `/admin/groups/${editor.row.id}/members`,
            'PUT',
            { userIds: values.getAll('memberIds').map(Number) },
          )
          break

        case 'access': {
          const access: Access[] = []
          for (const database of data.databases) {
            const level = values.get(`access-${database.id}`)
            if (level === 'view' || level === 'edit') {
              access.push({ databaseId: database.id, level })
            }
          }

          await adminApi(
            `/admin/groups/${editor.row.id}/access`,
            'PUT',
            { access },
          )
          break
        }

        case 'database': {
          const body: Record<string, unknown> = databaseBody(values)
          const password = String(values.get('password') ?? '')

          if (!editor.row || password !== '') {
            body.password = password
          }

          await adminApi(
            editor.row
              ? `/admin/databases/${editor.row.id}`
              : '/admin/databases',
            editor.row ? 'PATCH' : 'POST',
            body,
          )
          break
        }

        case 'delete':
          await adminApi(
            `/admin/${editor.resource}/${editor.id}`,
            'DELETE',
          )
          break
      }

      onSaved(
        editor.kind === 'delete'
          ? 'Data berhasil dihapus.'
          : 'Perubahan berhasil disimpan.',
      )
    } catch (err) {
      setError(adminErrorMessage(err))
    } finally {
      operationPending.current = false
      setBusy(false)
    }
  }

  const ownAccount =
    editor.kind === 'user' &&
    editor.row !== undefined &&
    editor.row.id === currentUserId

  return (
    <dialog
      ref={dialogRef}
      className="admin-dialog"
      aria-labelledby="admin-dialog-title"
      onCancel={(event) => {
        event.preventDefault()
        if (!busy) onClose()
      }}
    >
      <form ref={formRef} onSubmit={submit}>
        <header className="admin-dialog-header">
          <h2 id="admin-dialog-title">{editorTitle(editor)}</h2>
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            aria-label="Tutup dialog"
          >
            Tutup
          </button>
        </header>

        <div className="admin-dialog-body">
          {error && <p className="admin-alert error" role="alert">{error}</p>}
          {testResult && (
            <p className="admin-alert success" role="status">{testResult}</p>
          )}

          <fieldset disabled={busy} className="admin-form-fields">
            {editor.kind === 'user' && (
              <>
                <Field label="Username">
                  <input
                    name="username"
                    defaultValue={editor.row?.username ?? ''}
                    required
                    maxLength={100}
                    autoComplete="off"
                  />
                </Field>

                <Field label={editor.row ? 'Password baru (opsional)' : 'Password'}>
                  <input
                    name="password"
                    type="password"
                    required={!editor.row}
                    minLength={8}
                    autoComplete="new-password"
                  />
                </Field>
                <p className="admin-help">
                  Minimal 8 karakter, maksimal 72 byte.
                  {editor.row && ' Kosongkan untuk mempertahankan password.'}
                </p>

                <label className="admin-check">
                  <input
                    name="isAdmin"
                    type="checkbox"
                    defaultChecked={editor.row?.isAdmin ?? false}
                    disabled={ownAccount}
                  />
                  Platform admin
                </label>

                {editor.row && (
                  <label className="admin-check">
                    <input
                      name="isActive"
                      type="checkbox"
                      defaultChecked={editor.row.isActive}
                      disabled={ownAccount}
                    />
                    Akun aktif
                  </label>
                )}

                {ownAccount && (
                  <p className="admin-help">
                    Status aktif dan hak admin akun sendiri tidak dapat diturunkan.
                  </p>
                )}
              </>
            )}

            {editor.kind === 'group' && (
              <>
                <Field label="Nama group">
                  <input
                    name="name"
                    defaultValue={editor.row?.name ?? ''}
                    required
                    maxLength={100}
                  />
                </Field>
                <Field label="Deskripsi">
                  <textarea
                    name="description"
                    defaultValue={editor.row?.description ?? ''}
                    rows={3}
                    maxLength={2000}
                  />
                </Field>
              </>
            )}

            {editor.kind === 'members' && (
              <>
                <p className="admin-help">
                  Centang semua user yang harus menjadi anggota group ini.
                </p>
                {data.users.length === 0 && <p>Belum ada user.</p>}
                <div className="admin-choice-list">
                  {data.users.map((user) => (
                    <label className="admin-choice" key={user.id}>
                      <input
                        type="checkbox"
                        name="memberIds"
                        value={user.id}
                        defaultChecked={editor.row.memberIds.includes(user.id)}
                      />
                      <span>
                        <strong>{user.username}</strong>
                        <small>{user.isActive ? 'Aktif' : 'Nonaktif'}</small>
                      </span>
                    </label>
                  ))}
                </div>
              </>
            )}

            {editor.kind === 'access' && (
              <>
                <p className="admin-help">
                  Izin berlaku untuk seluruh anggota group. Akses efektif user
                  mengikuti level tertinggi dari semua group-nya.
                </p>
                {data.databases.length === 0 && <p>Belum ada koneksi database.</p>}
                {data.databases.map((database) => (
                  <Field key={database.id} label={database.name}>
                    <select
                      name={`access-${database.id}`}
                      defaultValue={
                        editor.row.access.find(
                          (entry) => entry.databaseId === database.id,
                        )?.level ?? ''
                      }
                    >
                      <option value="">Tidak ada akses</option>
                      <option value="view">View Only</option>
                      <option value="edit">View + Edit</option>
                    </select>
                  </Field>
                ))}
              </>
            )}

            {editor.kind === 'database' && (
              <>
                <Field label="Nama koneksi">
                  <input
                    name="name"
                    defaultValue={editor.row?.name ?? ''}
                    required
                    maxLength={100}
                  />
                </Field>
                <div className="admin-form-grid">
                  <Field label="Host">
                    <input
                      name="host"
                      defaultValue={editor.row?.host ?? ''}
                      placeholder="localhost"
                      required
                      maxLength={255}
                    />
                  </Field>
                  <Field label="Port">
                    <input
                      name="port"
                      type="number"
                      min={1}
                      max={65535}
                      defaultValue={editor.row?.port ?? 5432}
                      required
                    />
                  </Field>
                </div>
                <Field label="Nama database">
                  <input
                    name="dbname"
                    defaultValue={editor.row?.dbname ?? ''}
                    required
                    maxLength={255}
                  />
                </Field>
                <Field label="Username database">
                  <input
                    name="username"
                    defaultValue={editor.row?.username ?? ''}
                    required
                    maxLength={255}
                    autoComplete="off"
                  />
                </Field>
                <Field label={editor.row ? 'Password baru (opsional)' : 'Password database'}>
                  <input name="password" type="password" autoComplete="new-password" />
                </Field>
                {editor.row && (
                  <p className="admin-help">
                    Kosongkan untuk mempertahankan password tersimpan.
                    Untuk test connection, masukkan password koneksi.
                  </p>
                )}
                <Field label="SSL mode">
                  <select name="sslmode" defaultValue={editor.row?.sslmode ?? 'disable'}>
                    {['disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full'].map(
                      (mode) => <option key={mode} value={mode}>{mode}</option>,
                    )}
                  </select>
                </Field>
              </>
            )}

            {editor.kind === 'delete' && (
              <>
                <p>Hapus <strong>{editor.name}</strong>?</p>
                <p className="admin-help">
                  {editor.resource === 'users' &&
                    'Akun, session, dan keanggotaan group akan dihapus. User yang terkait riwayat migration harus dinonaktifkan.'}
                  {editor.resource === 'groups' &&
                    'Group beserta keanggotaan dan pemberian aksesnya akan dihapus.'}
                  {editor.resource === 'databases' &&
                    'Catatan koneksi, pemberian akses, dan riwayat migration di platform akan dihapus. Database PostgreSQL target tidak dihapus.'}
                </p>
              </>
            )}
          </fieldset>
        </div>

        <footer className="admin-dialog-footer">
          {editor.kind === 'database' && (
            <button type="button" onClick={testConnection} disabled={busy}>
              Test connection
            </button>
          )}
          <button type="button" onClick={onClose} disabled={busy}>Batal</button>
          <button
            type="submit"
            className={editor.kind === 'delete' ? 'danger' : 'primary'}
            disabled={busy}
          >
            {busy ? 'Memproses...' : editor.kind === 'delete' ? 'Hapus' : 'Simpan'}
          </button>
        </footer>
      </form>
    </dialog>
  )
}

export default function AdminPage({
  currentUserId,
}: {
  currentUserId?: number
}) {
  const [section, setSection] = useState<Section>('users')
  const [search, setSearch] = useState('')
  const [data, setData] = useState<Data>({
    users: [],
    groups: [],
    databases: [],
  })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [revision, setRevision] = useState(0)
  const [editor, setEditor] = useState<Editor | null>(null)

  useEffect(() => {
    let active = true

    async function load() {
      setLoading(true)
      setError('')

      try {
        const [users, groups, databases] = await Promise.all([
          adminApi<User[]>('/admin/users'),
          adminApi<Group[]>('/admin/groups'),
          adminApi<Database[]>('/admin/databases'),
        ])

        if (active) setData({ users, groups, databases })
      } catch (err) {
        if (active) setError(adminErrorMessage(err))
      } finally {
        if (active) setLoading(false)
      }
    }

    void load()
    return () => { active = false }
  }, [revision])

  const current = sections.find((item) => item.id === section)!
  const query = search.trim().toLowerCase()

  const users = data.users.filter((user) =>
    user.username.toLowerCase().includes(query),
  )
  const groups = data.groups.filter((group) =>
    `${group.name} ${group.description}`.toLowerCase().includes(query),
  )
  const databases = data.databases.filter((database) =>
    `${database.name} ${database.host} ${database.dbname}`.toLowerCase().includes(query),
  )

  function add() {
    if (section === 'users') setEditor({ kind: 'user' })
    if (section === 'groups') setEditor({ kind: 'group' })
    if (section === 'databases') setEditor({ kind: 'database' })
  }

  function remove(resource: Section, id: number, name: string) {
    setEditor({ kind: 'delete', resource, id, name })
  }

  return (
    <section className="admin-page">
      <header className="admin-page-heading">
        <div>
          <p className="admin-eyebrow">Platform Admin</p>
          <h1>Pengelolaan platform</h1>
          <p className="admin-muted">
            Kelola akun, group, dan koneksi database dalam satu tempat.
          </p>
        </div>
      </header>

      <nav className="admin-tabs" aria-label="Bagian administrasi">
        {sections.map((item) => (
          <button
            key={item.id}
            type="button"
            aria-current={section === item.id ? 'page' : undefined}
            onClick={() => {
              setSection(item.id)
              setSearch('')
              setNotice('')
            }}
          >
            {item.label}
          </button>
        ))}
      </nav>

      {notice && <p role="status" className="admin-alert success">{notice}</p>}
      {error && <p role="alert" className="admin-alert error">{error}</p>}

      <div className="admin-toolbar">
        <div>
          <h2>{current.label}</h2>
          <p className="admin-muted">{current.description}</p>
        </div>
        <div className="admin-actions">
          <button
            type="button"
            disabled={loading}
            onClick={() => setRevision((value) => value + 1)}
          >
            Muat ulang
          </button>
          <button
            type="button"
            className="primary"
            disabled={loading || Boolean(error)}
            onClick={add}
          >
            Tambah {section === 'databases' ? 'koneksi' : current.label.toLowerCase()}
          </button>
        </div>
      </div>

      <label className="admin-search">
        <span>Cari {current.label.toLowerCase()}</span>
        <input
          type="search"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={`Cari ${current.label.toLowerCase()}...`}
        />
      </label>

      {loading ? (
        <p role="status" className="admin-empty">Memuat data...</p>
      ) : error ? (
        <p className="admin-empty">
          Data belum dapat ditampilkan. Gunakan tombol Muat ulang untuk mencoba kembali.
        </p>
      ) : (
        <>
          {section === 'users' && (
            <Table headings={['Username', 'Peran', 'Status', 'Tindakan']} empty={users.length === 0}>
              {users.map((user) => (
                <tr key={user.id}>
                  <td>
                    <strong>{user.username}</strong>
                    {user.id === currentUserId && <small className="admin-muted">Akun Anda</small>}
                  </td>
                  <td>{user.isAdmin ? 'Platform admin' : 'User'}</td>
                  <td>{user.isActive ? 'Aktif' : 'Nonaktif'}</td>
                  <td>
                    <div className="admin-actions">
                      <button onClick={() => setEditor({ kind: 'user', row: user })}>
                        Edit
                      </button>
                      <button
                        className="danger-outline"
                        disabled={user.id === currentUserId}
                        onClick={() => remove('users', user.id, user.username)}
                      >
                        Hapus
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </Table>
          )}

          {section === 'groups' && (
            <Table headings={['Group', 'Anggota', 'Akses database', 'Tindakan']} empty={groups.length === 0}>
              {groups.map((group) => (
                <tr key={group.id}>
                  <td>
                    <strong>{group.name}</strong>
                    <small className="admin-muted">{group.description || 'Tanpa deskripsi'}</small>
                  </td>
                  <td>{group.memberIds.length} user</td>
                  <td>{group.access.length} database</td>
                  <td>
                    <div className="admin-actions">
                      <button onClick={() => setEditor({ kind: 'group', row: group })}>Edit</button>
                      <button onClick={() => setEditor({ kind: 'members', row: group })}>Anggota</button>
                      <button onClick={() => setEditor({ kind: 'access', row: group })}>Akses</button>
                      <button
                        className="danger-outline"
                        onClick={() => remove('groups', group.id, group.name)}
                      >
                        Hapus
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </Table>
          )}

          {section === 'databases' && (
            <Table headings={['Koneksi', 'Host', 'Database', 'SSL', 'Tindakan']} empty={databases.length === 0}>
              {databases.map((database) => (
                <tr key={database.id}>
                  <td><strong>{database.name}</strong></td>
                  <td>{database.host}:{database.port}</td>
                  <td>
                    {database.dbname}
                    <small className="admin-muted">{database.username}</small>
                  </td>
                  <td>{database.sslmode}</td>
                  <td>
                    <div className="admin-actions">
                      <button onClick={() => setEditor({ kind: 'database', row: database })}>
                        Edit / Test
                      </button>
                      <button
                        className="danger-outline"
                        onClick={() => remove('databases', database.id, database.name)}
                      >
                        Hapus
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </Table>
          )}
        </>
      )}

      {editor && (
        <EditorDialog
          editor={editor}
          data={data}
          currentUserId={currentUserId}
          onClose={() => setEditor(null)}
          onSaved={(message) => {
            setEditor(null)
            setNotice(message)
            setRevision((value) => value + 1)
          }}
        />
      )}
    </section>
  )
}