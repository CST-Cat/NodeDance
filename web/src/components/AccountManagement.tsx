import { useEffect, useRef, useState } from "react";
import { operationRequest, type User } from "../api";
import { requestConfirm } from "../dialogs";
import { t } from "../i18n";
import UiSelect from "./UiSelect";
import UiDialog from "./UiDialog";
import "./AccountMenu.css";

export default function AccountManagement({ currentId }: { currentId: number }) {
  const [accounts, setAccounts] = useState<User[]>([]), [editing, setEditing] = useState<User | "new" | null>(null);
  const [reset, setReset] = useState<User | null>(null), [password, setPassword] = useState("");
  const [username, setUsername] = useState(""), [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState("viewer"), [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => { const pending = new AbortController(); controller.current = pending; void load(pending.signal); return () => { pending.abort(); }; }, []);
  async function load(signal = controller.current?.signal) {
    try { const result = await operationRequest<{accounts:User[]}>("accounts.list", {}, { signal }); if (!signal?.aborted) setAccounts(result.accounts); }
    catch (e) { if (!signal?.aborted) setError(e instanceof Error ? e.message : String(e)); }
  }
  function edit(account: User | "new") { setEditing(account); setReset(null); setPassword(""); setUsername(account === "new" ? "" : account.username); setDisplayName(account === "new" ? "" : account.displayName); setRole(account === "new" ? "viewer" : account.role); setEnabled(account === "new" || account.enabled); setError(""); setNotice(""); }
  function closeEditor() { if (!busy) { setEditing(null); setPassword(""); setError(""); } }
  async function execute(name: string, id: number | null, body: unknown) {
    const signal = controller.current?.signal;
    if (signal?.aborted) return;
    setBusy(true); setError(""); setNotice("");
    try {
      await operationRequest(name, id === null ? {} : { params: { id: String(id) } }, { body: JSON.stringify(body), signal });
      if (signal?.aborted) return;
      if (id === currentId) { location.reload(); return; }
      setEditing(null); setReset(null); setNotice(t("account.saved")); await load(signal);
    } catch (e) { if (!signal?.aborted) setError(e instanceof Error ? e.message : String(e)); }
    finally { setPassword(""); if (!signal?.aborted) setBusy(false); }
  }
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (!editing || busy) return;
    if (editing !== "new" && !await requestConfirm(t("account.confirmUpdate"))) return;
    if (editing === "new") await execute("accounts.create", null, { username, displayName, role, password });
    else await execute("accounts.update", editing.id, { username, displayName, role, enabled, confirmed: true });
  }
  return <section className="surface-card settings-card settings-list-card account-management" aria-labelledby="accounts-heading">
    <div className="settings-list-heading account-management-heading">
      <h3 id="accounts-heading">{t("account.manage")}</h3>
      <div className="account-actions">
        <button type="button" disabled={busy} onClick={() => { setError(""); void load(); }}>{t("account.refresh")}</button>
        <button type="button" disabled={busy} onClick={() => edit("new")}>{t("account.create")}</button>
      </div>
    </div>
    <div className="settings-list-body">
    {error && !editing ? <p role="alert" className="account-error">{error}</p> : null}
    {notice ? <p role="status">{notice}</p> : null}
    <div className="settings-record-list account-management-list">{accounts.map(account => <article key={account.id} className="settings-record-row account-management-row">
      <div className="settings-record-info workspace-account-copy"><strong>{account.displayName}</strong><span>{account.username} · {t(`account.role.${account.role}`)}{!account.enabled ? ` · ${t("account.disabled")}` : ""}</span></div>
      <div className="account-actions">
        <button type="button" disabled={busy} onClick={() => { setReset(account); setEditing(null); setPassword(""); setError(""); }}>{t("account.resetPassword")}</button>
        <button type="button" disabled={busy} onClick={() => edit(account)}>{t("account.edit")}</button>
      </div>
    </article>)}</div>
    {editing ? <UiDialog open title={t(editing === "new" ? "account.create" : "account.edit")} busy={busy} onClose={closeEditor} className="account-dialog">
    {error ? <p role="alert" className="account-error">{error}</p> : null}
    <form className="account-edit-form" onSubmit={save}>
      <div className="account-name-fields">
      <label className="field"><span>{t("auth.username")}</span><input name="accountUsername" autoComplete="off" required value={username} disabled={busy} onChange={e => setUsername(e.target.value)} /></label>
      <label className="field"><span>{t("account.displayName")}</span><input name="accountDisplayName" required value={displayName} disabled={busy} onChange={e => setDisplayName(e.target.value)} /></label>
      </div>
      {editing === "new" ? <label className="field"><span>{t("account.password")}</span><input name="accountPassword" type="password" autoComplete="new-password" required value={password} disabled={busy} onChange={e => setPassword(e.target.value)} /></label> : null}
      <div className="account-edit-footer"><label className="field account-role-field"><span>{t("account.role")}</span><UiSelect modelValue={role} onUpdateModelValue={(value:string) => setRole(value)} disabled={busy}><option value="viewer">{t("account.role.viewer")}</option><option value="administrator">{t("account.role.administrator")}</option></UiSelect></label>{editing !== "new" ? <label className="account-enabled"><input type="checkbox" checked={enabled} disabled={busy} onChange={e => setEnabled(e.target.checked)} />{t("account.enabled")}</label> : null}<div className="account-actions"><button type="button" disabled={busy} onClick={closeEditor}>{t("account.cancel")}</button><button type="submit" disabled={busy}>{t("account.save")}</button>{editing !== "new" ? <button type="button" disabled={busy} onClick={async () => { const account = editing; if (busy) return; if (await requestConfirm(t("account.confirmDelete", { "0": account.displayName }))) await execute("accounts.delete", account.id, {confirmed:true}); }}>{t("account.delete")}</button> : null}</div></div>
    </form></UiDialog> : null}
    {reset ? <form className="account-edit-form" onSubmit={async event => { event.preventDefault(); if (!busy && await requestConfirm(t("account.confirmReset", {"0":reset.displayName}))) await execute("accounts.reset-password", reset.id, {password,confirmed:true}); }}>
      <label className="field"><span>{t("account.resetFor", { "0":reset.displayName })}</span><input name="resetPassword" type="password" autoComplete="new-password" required value={password} disabled={busy} onChange={e => setPassword(e.target.value)} /></label>
      <div className="account-actions"><button type="button" disabled={busy} onClick={() => {setReset(null);setPassword("");}}>{t("account.cancel")}</button><button type="submit" disabled={busy}>{t("account.resetPassword")}</button></div>
    </form> : null}
    </div>
  </section>;
}
