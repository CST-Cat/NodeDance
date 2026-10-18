import { useEffect, useRef, useState, type InputHTMLAttributes } from "react";
import { t } from "../i18n";
import UiIcon from "./UiIcon";
import "./UiPassword.css";

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, "type">;

export default function UiPassword(props: Props) {
  const [visible, setVisible] = useState(false);
  const container = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (!visible) return;
    const hide = () => setVisible(false);
    const timeout = window.setTimeout(hide, 5000);
    const form = container.current?.closest("form");
    const onVisibility = () => { if (document.hidden) hide(); };
    window.addEventListener("blur", hide);
    document.addEventListener("visibilitychange", onVisibility);
    form?.addEventListener("submit", hide, true);
    return () => {
      window.clearTimeout(timeout);
      window.removeEventListener("blur", hide);
      document.removeEventListener("visibilitychange", onVisibility);
      form?.removeEventListener("submit", hide, true);
    };
  }, [visible]);

  useEffect(() => { if (props.disabled) setVisible(false); }, [props.disabled]);

  return <span ref={container} className="ui-password"
    onBlur={event => {
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setVisible(false);
    }}>
    <input {...props} type={visible ? "text" : "password"} />
    <button type="button" className="password-visibility"
      aria-label={t(visible ? "auth.hidePassword" : "auth.showPassword")}
      title={t(visible ? "auth.hidePassword" : "auth.showPassword")}
      aria-pressed={visible} disabled={props.disabled}
      onClick={() => setVisible(value => !value)}>
      <UiIcon name={visible ? "eyeOff" : "eye"} />
    </button>
  </span>;
}
