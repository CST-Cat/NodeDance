import { useEffect, useId, useRef, useState } from "react";
import { operationRequest, type User } from "../api";
import { t } from "../i18n";
import UiIcon from "./UiIcon";
import "./AccountMenu.css";

export default function AccountMenu({ user, avatar, busy, onSettings, onLogout }: {
  user: User; avatar?: string; busy: boolean; onSettings: () => void; onLogout: () => void;
}) {
  const id = useId(), trigger = useRef<HTMLButtonElement>(null), panel = useRef<HTMLDivElement>(null);
  const pending = useRef<AbortController | null>(null);
  const [open, setOpen] = useState(false), [accounts, setAccounts] = useState<User[]>([]);
  const [selected, setSelected] = useState<User | null>(null), [password, setPassword] = useState("");
  const [error, setError] = useState(""), [loading, setLoading] = useState(false), [switching, setSwitching] = useState(false);
  const close = () => panel.current?.hidePopover();
  useEffect(() => {
    const dismiss = () => panel.current?.hidePopover();
    const resize = () => {
      if (!trigger.current?.getClientRects().length) { panel.current?.hidePopover(); return; }
      if (panel.current?.matches(":popover-open")) position();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || !panel.current?.matches(":popover-open")) return;
      event.preventDefault();
      event.stopPropagation();
      panel.current.hidePopover();
      trigger.current?.focus();
    };
    window.addEventListener("resize", resize); window.addEventListener("hashchange", dismiss);
    document.addEventListener("keydown", escape, true);
    return () => { pending.current?.abort(); window.removeEventListener("resize", resize); window.removeEventListener("hashchange", dismiss); document.removeEventListener("keydown", escape, true); };
  }, []);
  async function load() {
    pending.current?.abort(); const request = new AbortController(); pending.current = request;
    setLoading(true); setError("");
    try { const result = await operationRequest<{ accounts: User[] }>("accounts.list", {}, { signal: request.signal }); if (!request.signal.aborted) setAccounts(result.accounts); }
    catch (e) { if (!request.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }
    finally { if (!request.signal.aborted) setLoading(false); }
  }
  function position() {
    if (!trigger.current || !panel.current) return;
    const rect = trigger.current.getBoundingClientRect(), width = parseFloat(getComputedStyle(panel.current).width);
    Object.assign(panel.current.style, { left: `${Math.max(12, Math.min(rect.left, innerWidth - width - 12))}px`, bottom: `${innerHeight - rect.top + 8}px`, maxHeight: `${Math.max(100, rect.top - 20)}px` });
  }
  async function switchAccount(event: React.FormEvent) {
    event.preventDefault(); if (!selected || switching) return;
    setSwitching(true); setError("");
    const request = new AbortController(); pending.current?.abort(); pending.current = request;
    try { await operationRequest("auth.switch", {}, { body: JSON.stringify({ username: selected.username, password }), signal: request.signal }); if (!request.signal.aborted) location.reload(); }
    catch (e) { if (!request.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }
    finally { setPassword(""); if (!request.signal.aborted) setSwitching(false); }
  }
  return <div className="workspace-account">
    <button ref={trigger} type="button" className="account-trigger" popoverTarget={id} aria-haspopup="dialog" aria-expanded={open} aria-controls={id} aria-label={t("account.menu")} onClick={position}>
      <span className="profile-avatar">{avatar ? <img src={avatar} alt="" /> : <span aria-hidden="true">{user.displayName.slice(0, 1)}</span>}</span>
      <span className="workspace-account-copy"><strong>{user.displayName}</strong><span>{t(`account.role.${user.role}`)}</span></span>
      <span className="account-chevron" aria-hidden="true">⌃</span>
    </button>
    <div ref={panel} id={id} popover="auto" role="dialog" aria-label={t("account.menu")} className="account-panel surface-card" onToggle={event => {
      const expanded = event.newState === "open"; setOpen(expanded);
      if (expanded) { void load(); } else { pending.current?.abort(); setPassword(""); setSelected(null); setError(""); setSwitching(false); }
    }}>
      <p className="account-panel-title">{t("account.switch")}</p>
      {loading ? <p role="status">{t("account.loading")}</p> : <div className="account-grid">{accounts.map(account => <button key={account.id} type="button" className="account-card" aria-pressed={account.id === user.id} disabled={!account.enabled || switching} onClick={() => { if (account.id !== user.id) { setSelected(account); setError(""); setPassword(""); } }}>
        <span className="profile-avatar" aria-hidden="true">{account.displayName.slice(0, 1)}</span>
        <span className="workspace-account-copy"><strong>{account.displayName}</strong><span>{t(`account.role.${account.role}`)}{!account.enabled ? ` · ${t("account.disabled")}` : ""}</span></span>
        {account.id === user.id ? <UiIcon name="check" /> : null}
      </button>)}</div>}
      {error ? <p role="alert" className="account-error">{error}</p> : null}
      {!loading && error && !selected ? <button type="button" onClick={() => void load()}>{t("account.retry")}</button> : null}
      {selected ? <form className="account-switch-form" onSubmit={switchAccount}>
        <label className="field"><span>{t("account.verify", { "0": selected.displayName })}</span><input name="password" type="password" autoComplete="current-password" required autoFocus value={password} disabled={switching} onChange={event => setPassword(event.target.value)} /></label>
        <div className="account-actions"><button type="button" disabled={switching} onClick={() => { setSelected(null); setPassword(""); setError(""); }}>{t("account.cancel")}</button><button type="submit" disabled={switching}>{t("account.switch")}</button></div>
      </form> : null}
      <button type="button" onClick={() => { close(); onSettings(); }}><UiIcon name="settings" /><span>{t("nav.settings")}</span></button>
      <button type="button" disabled={busy || switching} onClick={() => { close(); onLogout(); }}><UiIcon name="logout" /><span>{t("m.3ab8cc15939f")}</span></button>
    </div>
  </div>;
}
