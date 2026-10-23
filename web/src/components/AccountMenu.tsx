import { useEffect, useRef, useState } from "react";
import { operationRequest, type User } from "../api";
import { t } from "../i18n";
import UiIcon from "./UiIcon";
import { UserRound } from "lucide-react";
import { UserMenu } from "./arc/user-menu/user-menu";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { Button } from "./arc/button/button";
import { Input } from "./arc/input/input";
import "./AccountMenu.css";

export default function AccountMenu({ user, avatar, busy, onSettings, onLogout }: {
 user: User; avatar?: string; busy: boolean; onSettings: () => void; onLogout: () => void;
}) {
 const trigger = useRef<HTMLButtonElement>(null), pending = useRef<AbortController | null>(null);
 const [container, setContainer] = useState<HTMLElement | null>(null);
 const [open, setOpen] = useState(false), [accounts, setAccounts] = useState<User[]>([]);
 const [selected, setSelected] = useState<User | null>(null), [password, setPassword] = useState("");
 const [error, setError] = useState(""), [loading, setLoading] = useState(false), [switching, setSwitching] = useState(false);
 const clear = () => { pending.current?.abort(); setPassword(""); setSelected(null); setError(""); setSwitching(false); };
 useEffect(() => {
   setContainer(trigger.current?.closest<HTMLElement>(".app-shell") ?? null);
   const dismiss = () => { setOpen(false); clear(); };
   const resize = () => { if (!trigger.current?.getClientRects().length) dismiss(); };
   window.addEventListener("hashchange", dismiss); window.addEventListener("resize", resize);
   return () => { pending.current?.abort(); window.removeEventListener("hashchange", dismiss); window.removeEventListener("resize", resize); };
 }, []);
  async function load() {
    pending.current?.abort(); const request = new AbortController(); pending.current = request;
    setLoading(true); setError("");
    try { const result = await operationRequest<{ accounts: User[] }>("accounts.list", {}, { signal: request.signal }); if (!request.signal.aborted) setAccounts(result.accounts); }
    catch (e) { if (!request.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }
    finally { if (!request.signal.aborted) setLoading(false); }
  }
  async function switchAccount(event: React.FormEvent) {
    event.preventDefault(); if (!selected || switching) return;
    setSwitching(true); setError("");
    const request = new AbortController(); pending.current?.abort(); pending.current = request;
    try { await operationRequest("auth.switch", {}, { body: JSON.stringify({ username: selected.username, password }), signal: request.signal }); if (!request.signal.aborted) location.reload(); }
    catch (e) { if (!request.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }
    finally { setPassword(""); if (!request.signal.aborted) setSwitching(false); }
  }

 return <div className="workspace-account product-arc arc-ui">
   <UserMenu ref={trigger} user={{name:user.displayName,email:t(`account.role.${user.role}`),avatarSrc:avatar}}
     className="account-trigger" menuClassName="account-panel" portalContainer={container} showName showTheme={false} align="start"
     open={open} onOpenChange={next => { setOpen(next); if(next) void load(); else { pending.current?.abort(); setPassword(""); setError(""); } }}
     disabled={busy || switching} labels={{accountMenu:t("account.menu"),signOut:t("m.3ab8cc15939f"),signingOut:t("m.ff509c9ba052")}}
     items={[
       ...(loading ? [{label:t("account.loading"),disabled:true}] : accounts.map(account => ({
         id:String(account.id), label:account.displayName,
         description:t(`account.role.${account.role}`)+(!account.enabled ? ` · ${t("account.disabled")}` : ""),
         current:account.id===user.id,
         icon:account.id===user.id ? <UiIcon name="check" /> : <UserRound size={16} aria-hidden="true" />,
         disabled:!account.enabled || account.id===user.id || switching,
         className:"account-choice",
         onSelect:() => {setSelected(account);setPassword("");setError("");},
       }))),
       ...(error && !selected ? [{label:error,disabled:true},{label:t("account.retry"),onSelect:() => {setOpen(true);void load();}}] : []),
       {label:t("nav.settings"),icon:<UiIcon name="settings" />,onSelect:onSettings},
     ]} onSignOut={onLogout} />
   <Dialog open={selected !== null} onOpenChange={next => {if(!next && !switching)clear();}}>
     <DialogContent title={t("account.switch")} container={container} className="account-switch-dialog product-arc arc-ui"
       onCloseAutoFocus={event => {event.preventDefault();trigger.current?.focus();}}>
       <form className="account-switch-form" onSubmit={switchAccount}>
         <Input label={t("account.verify",{"0":selected?.displayName ?? ""})} name="password" type="password" autoComplete="current-password" required autoFocus value={password} disabled={switching} onChange={event=>setPassword(event.target.value)} />
         {error ? <p role="alert" className="account-error">{error}</p>:null}
         <div className="account-actions"><Button type="button" disabled={switching} onClick={clear}>{t("account.cancel")}</Button><Button type="submit" disabled={switching}>{t("account.switch")}</Button></div>
       </form>
     </DialogContent>
   </Dialog>
 </div>;
}
