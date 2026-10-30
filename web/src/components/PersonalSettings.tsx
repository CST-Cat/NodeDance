import MonitoringSettings from "./MonitoringSettings";
import { SettingsGroup, SettingsRows } from "./SettingsGroup";
function SettingsPassword(
  props: Omit<InputHTMLAttributes<HTMLInputElement>, "type">,
) {
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const hide = () =>
      element
        .querySelector<HTMLButtonElement>('button[aria-pressed="true"]')
        ?.click();
    const synchronize = () => {
      clearTimeout(timer);
      if (element.querySelector('input[type="text"]'))
        timer = setTimeout(hide, 5000);
    };
    const observer = new MutationObserver(synchronize);
    observer.observe(element, {
      subtree: true,
      attributes: true,
      attributeFilter: ["type"],
    });
    const visibility = () => {
      if (document.hidden) hide();
    };
    const form = element.closest("form");
    window.addEventListener("blur", hide);
    document.addEventListener("visibilitychange", visibility);
    form?.addEventListener("submit", hide, true);
    return () => {
      observer.disconnect();
      clearTimeout(timer);
      window.removeEventListener("blur", hide);
      document.removeEventListener("visibilitychange", visibility);
      form?.removeEventListener("submit", hide, true);
    };
  }, []);
  return (
    <span
      ref={root}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null))
          event.currentTarget
            .querySelector<HTMLButtonElement>('button[aria-pressed="true"]')
            ?.click();
      }}
    >
      <PasswordField
        {...props}
        label=""
        messages={{
          showPassword: t("auth.showPassword"),
          hidePassword: t("auth.hidePassword"),
        }}
      />
    </span>
  );
}

import type { Appearance, Session, User } from "../api";
import {
  t,
  display,
  languagePreference,
  setLanguagePreference,
  type LanguagePreference,
} from "../i18n";
import { cell, derived, lazyView, useController, watchState } from "../state";
import { createGuestPublication } from "../guest-publication";
import { describeSessionDevice } from "../session-device";
import {
  ManagementPage,
  Button,
  Input,
  Checkbox,
  Select,
  Alert,
} from "./ManagementUI";
import { Avatar } from "./arc/avatar/avatar";

import SegmentedControl from "./arc/segmented-control/segmented-control";
import { MultiSelect } from "./arc/multi-select/multi-select";
import { useEffect, useRef, type InputHTMLAttributes } from "react";
import { PasswordField } from "./arc/password-field/password-field";
import AccountManagement from "./AccountManagement";
import { DEFAULT_SHARKS } from "../api";
import type { SharkKind, SharkSpec } from "../vendor/ascii-sharks/world";

const SHARK_KINDS: SharkKind[] = [
  "classic",
  "hammerhead",
  "zebra",
  "tiger",
  "greathammer",
  "mako",
  "thresher",
  "nurse",
];
const GuestDisplay = lazyView(() => import("./GuestDisplay"));
import "./PersonalSettings.css";

const SHARK_OPTIONS: { kind: SharkKind; key: string }[] = SHARK_KINDS.map(
  (kind) => ({ kind, key: `settings.asciiSharks.species.${kind}` }),
);

type Value<T> = { readonly value: T };
export interface PersonalSettingsModel {
  currentUser: Value<User | null>;
  appearance: Appearance;
  passwordForm: {
    currentPassword: string;
    newPassword: string;
    confirmPassword: string;
  };
  sessions: Value<Session[]>;
  busy: Value<boolean>;
  busyLabel: Value<string>;
  uploading: Value<string>;
  avatarSrc: Value<string>;
  avatarRevision: Value<number>;
  visibleName: Value<string>;
  notice: Value<string>;
  error: Value<string>;
  saveProfile: () => Promise<void>;
  saveSharks: (sharks: SharkSpec[]) => Promise<void>;
  changePassword: () => Promise<void>;
  loadSessions: () => Promise<void>;
  revokeSession: (session: Session) => Promise<void>;
  handleImageChange: (
    kind: "avatar" | "background",
    event: Event,
  ) => Promise<void>;
  formatTime: (value: string) => string;
}
export default function PersonalSettings(input: {
  model: PersonalSettingsModel;
}) {
  const profileForm = useRef<HTMLFormElement>(null);
  useEffect(() => {
    const form = profileForm.current;
    const group = form?.querySelector<HTMLElement>(
      '.theme-options > [role="group"]',
    );
    if (!form || !group) return;
    const synchronize = () => {
      form.style.setProperty(
        "--appearance-control-width",
        `${group.getBoundingClientRect().width}px`,
      );
    };
    synchronize();
    const observer = new ResizeObserver(synchronize);
    observer.observe(group);
    return () => observer.disconnect();
  }, []);
  return useController(
    (props: { model: PersonalSettingsModel }) => {
      const model = props.model;
      const sessionsRefreshing = cell(false),
        sessionsError = cell("");
      const sharkRoster = cell<SharkSpec[]>(
        model.appearance.sharks.map((shark) => ({ ...shark })),
      );
      const sharkSizeDrafts = cell<Record<number, string>>({});
      function setSharkRoster(next: SharkSpec[]) {
        sharkRoster.value = next;
      }
      function updateShark(index: number, next: Partial<SharkSpec>) {
        setSharkRoster(
          sharkRoster.value.map((shark, current) =>
            current === index ? { ...shark, ...next } : shark,
          ),
        );
      }
      function addShark() {
        const current = sharkRoster.value;
        sharkSizeDrafts.value = {};
        setSharkRoster([
          ...current,
          { kind: "classic", size: Math.max(0.1, 1 - current.length * 0.1) },
        ]);
      }
      function removeShark(index: number) {
        sharkSizeDrafts.value = {};
        setSharkRoster(
          sharkRoster.value.filter(
            (_, current) => current !== index,
          ),
        );
      }
      async function refreshSessions() {
        sessionsRefreshing.value = true;
        sessionsError.value = "";
        try {
          await model.loadSessions();
        } catch (reason) {
          sessionsError.value =
            reason instanceof Error ? reason.message : String(reason);
        } finally {
          sessionsRefreshing.value = false;
        }
      }

      const publication = createGuestPublication(
        () => model.currentUser.value?.role === "administrator",
      );
      watchState(
        derived(() => model.currentUser.value?.id),
        () => {
          publication.reset();
          if (model.currentUser.value?.role === "administrator")
            void publication.load();
        },
      );
      return () => {
        const admin = model.currentUser.value?.role === "administrator";
        const policy = publication.settings.value;
        const resources = [
          ...publication.nodeChoices.value.map((node) => ({
            kind: "nodes" as const,
            id: node.nodeId,
            label: node.displayName,
          })),
          ...publication.probeChoices.value.map((probe) => ({
            kind: "probes" as const,
            id: probe.id,
            label: probe.name,
          })),
        ];
        const sharks = sharkRoster.value;
        const sharkOptions = SHARK_OPTIONS.map(({ kind, key }) => ({
          value: kind,
          label: t(key),
        }));
        return (
          <ManagementPage
            registryStyles
            className="personal-settings workspace-settings"
            data-testid="personal-settings"
          >
            <header className="workspace-heading">
              <div>
                <h1>{t("nav.settings")}</h1>
              </div>
            </header>
            {model.notice.value ? (
              <Alert role="status" title={t("management.notice")}>
                {display(model.notice.value)}
              </Alert>
            ) : null}
            {model.error.value ? (
              <Alert role="alert" tone="danger" title={t("management.error")}>
                {display(model.error.value)}
              </Alert>
            ) : null}
            <div className="settings-sections">
              <section
                className="workspace-section settings-section profile-settings"
                aria-labelledby="appearance-heading"
              >
                <div className="workspace-section-heading">
                  <h2 id="appearance-heading">{t("settings.profileTab")}</h2>
                </div>
                <form
                  ref={profileForm}
                  className="profile-form"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void model.saveProfile();
                  }}
                >
                  <SettingsGroup>
                    <div className="profile-edit-row">
                      <Avatar
                        key={model.avatarRevision.value}
                        name={model.visibleName.value}
                        src={model.avatarSrc.value || undefined}
                        size="lg"
                      />
                      <div className="profile-identity">
                        <strong>{model.currentUser.value?.displayName}</strong>
                        <span>{model.currentUser.value?.username}</span>
                      </div>
                      <Button
                        type="button"
                        variant="secondary"
                        disabled={!!model.uploading.value}
                        onClick={() =>
                          document
                            .getElementById("settings-avatar-file")
                            ?.click()
                        }
                      >
                        {t("m.42ece898483f")}
                      </Button>
                      <Input
                        id="settings-avatar-file"
                        hidden
                        type="file"
                        aria-label={t("m.d7d2344d545d")}
                        accept="image/png,image/jpeg,image/gif"
                        disabled={!!model.uploading.value}
                        onChange={(event) =>
                          void model.handleImageChange(
                            "avatar",
                            event as unknown as Event,
                          )
                        }
                      />
                    </div>
                    <div className="parameter-row">
                      <label htmlFor="personalsettings-field-1">
                        {t("m.1264cd049636")}
                      </label>
                      <div className="appearance-control">
                        <Input
                          id="personalsettings-field-1"
                          name="profileDisplayName"
                          autoComplete="nickname"
                          required
                          value={model.appearance.displayName}
                          onChange={(event) => {
                            model.appearance.displayName = event.target.value;
                          }}
                        />
                      </div>
                    </div>
                    <div className="parameter-row">
                      <span>{t("m.8f29293906e7")}</span>
                      <div className="parameter-control theme-options">
                        <SegmentedControl
                          label={t("m.8f29293906e7")}
                          value={model.appearance.theme}
                          onValueChange={(value) => {
                            model.appearance.theme =
                              value as Appearance["theme"];
                          }}
                          options={[
                            { value: "system", label: t("m.217cfe7db1e3") },
                            { value: "dark", label: t("m.a6b75d068032") },
                            { value: "light", label: t("m.aa0819dfc4d8") },
                          ]}
                        />
                      </div>
                    </div>
                    <div className="profile-preferences-row">
                      <SettingsRows>
                        <div className="parameter-row profile-language-field">
                          <span>{t("settings.language")}</span>
                          <div className="parameter-select">
                            {" "}
                            <Select
                              aria-label={t("settings.language")}
                              data-testid="profile-language"
                              value={languagePreference.value}
                              onValueChange={(value) =>
                                setLanguagePreference(
                                  value as LanguagePreference,
                                )
                              }
                              options={[
                                {
                                  value: "system",
                                  label: t("settings.language.system"),
                                },
                                {
                                  value: "zh-CN",
                                  label: t("settings.language.zh-CN"),
                                },
                                {
                                  value: "en",
                                  label: t("settings.language.en"),
                                },
                              ]}
                            />
                          </div>
                        </div>
                        {admin ? (
                          <div className="parameter-row">
                            <span>{t("settings.loginBackground")}</span>
                            <div className="parameter-select">
                              <Select
                                aria-label={t("settings.loginBackground")}
                                data-testid="login-background-mode"
                                value={model.appearance.backgroundMode}
                                onValueChange={(value) => {
                                  model.appearance.backgroundMode =
                                    value as Appearance["backgroundMode"];
                                }}
                                options={[
                                  {
                                    value: "shark",
                                    label: t("settings.loginBackground.shark"),
                                  },
                                  {
                                    value: "image",
                                    label: t("settings.loginBackground.image"),
                                  },
                                ]}
                              />
                            </div>
                          </div>
                        ) : null}
                      </SettingsRows>
                    </div>
                    {admin && model.appearance.backgroundMode === "image" ? (
                      <div className="background-upload-row">
                        <div className="upload-copy">
                          <strong>{t("m.99cf84163dfc")}</strong>
                        </div>
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={!!model.uploading.value}
                          onClick={() =>
                            document
                              .getElementById("settings-background-file")
                              ?.click()
                          }
                        >
                          {t("m.9fbb4750bb39")}
                        </Button>
                        <Input
                          id="settings-background-file"
                          hidden
                          type="file"
                          aria-label={t("m.276341ecaf37")}
                          accept="image/png,image/jpeg,image/gif"
                          disabled={!!model.uploading.value}
                          onChange={(event) =>
                            void model.handleImageChange(
                              "background",
                              event as unknown as Event,
                            )
                          }
                        />
                      </div>
                    ) : null}
                  </SettingsGroup>
                  <div className="form-actions">
                    <Button
                      type="submit"
                      className="primary-button"
                      disabled={model.busy.value}
                    >
                      {model.busy.value
                        ? model.busyLabel.value
                        : t("m.200fec26bd0e")}
                    </Button>
                  </div>
                </form>
              </section>
              {admin && model.appearance.backgroundMode === "shark" ? <section
                className="workspace-section settings-section ascii-sharks-settings"
                aria-labelledby="ascii-sharks-heading"
              >
                <div className="workspace-section-heading">
                  <h2 id="ascii-sharks-heading">
                    {t("settings.asciiSharks.title")}
                  </h2>
                  <div className="workspace-actions">
                    <Button
                      type="button"
                      variant="secondary"
                      onClick={() => {
                        sharkSizeDrafts.value = {};
                        setSharkRoster(DEFAULT_SHARKS.map((shark) => ({ ...shark })));
                      }}
                    >
                      {t("settings.asciiSharks.reset")}
                    </Button>
                    <Button
                      type="button"
                      variant="secondary"
                      onClick={addShark}
                    >
                      {t("settings.asciiSharks.add")}
                    </Button>
                  </div>
                </div>
                <SettingsGroup className="ascii-sharks-group">
                  <p className="ascii-sharks-hint">
                    {t("settings.asciiSharks.hint")}
                  </p>
                  {sharks.length ? (
                    sharks.map((shark, index) => (
                      <div className="ascii-sharks-row" key={index}>
                        <span>
                          {t("settings.asciiSharks.animal", {
                            "0": index + 1,
                          })}
                        </span>
                        <div className="ascii-sharks-controls">
                          <Select
                            aria-label={`${t("settings.asciiSharks.species")} ${index + 1}`}
                            value={shark.kind}
                            options={sharkOptions}
                            onValueChange={(value) =>
                              updateShark(index, { kind: value as SharkKind })
                            }
                          />
                          <Input
                            type="number"
                            step="any"
                            aria-label={`${t("settings.asciiSharks.size")} ${index + 1}`}
                            value={
                              sharkSizeDrafts.value[index] ??
                              String(shark.size)
                            }
                            onChange={(event) => {
                              sharkSizeDrafts.value = {
                                ...sharkSizeDrafts.value,
                                [index]: event.target.value,
                              };
                            }}
                            onBlur={(event) => {
                              const draft = event.target.value;
                              if (
                                draft !== "" &&
                                Number.isFinite(Number(draft)) &&
                                Number(draft) > 0
                              )
                                updateShark(index, { size: Number(draft) });
                              const next = { ...sharkSizeDrafts.value };
                              delete next[index];
                              sharkSizeDrafts.value = next;
                            }}
                          />
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            aria-label={t("settings.asciiSharks.removeAnimal", {
                              "0": index + 1,
                            })}
                            onClick={() => removeShark(index)}
                          >
                            {t("settings.asciiSharks.removeAction")}
                          </Button>
                        </div>
                      </div>
                    ))
                  ) : (
                    <p className="ascii-sharks-empty" role="status">
                      {t("settings.asciiSharks.empty")}
                    </p>
                  )}
                </SettingsGroup>
                <div className="form-actions">
                  <Button
                    type="button"
                    className="primary-button"
                    disabled={model.busy.value}
                    onClick={() => void model.saveSharks(sharks)}
                  >
                    {model.busy.value
                      ? model.busyLabel.value
                      : t("settings.asciiSharks.save")}
                  </Button>
                </div>
              </section> : null}

              <section
                className="workspace-section settings-section security-settings"
                aria-labelledby="security-heading"
              >
                <div className="workspace-section-heading">
                  <h2 id="security-heading">{t("m.1b5365b35720")}</h2>
                </div>
                <form
                  className="password-form"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void model.changePassword();
                  }}
                >
                  <SettingsGroup>
                    <div className="parameter-row">
                      <label htmlFor="settings-currentPassword">
                        {t("m.a114cfb687e6")}
                      </label>
                      <SettingsPassword
                        id="settings-currentPassword"
                        name="currentPassword"
                        autoComplete="current-password"
                        required
                        value={model.passwordForm.currentPassword}
                        onChange={(event) => {
                          model.passwordForm.currentPassword =
                            event.target.value;
                        }}
                      />
                    </div>
                    <div className="parameter-row">
                      <label htmlFor="settings-newPassword">
                        {t("m.515e9c7cf7b2")}
                      </label>
                      <SettingsPassword
                        id="settings-newPassword"
                        name="newPassword"
                        autoComplete="new-password"
                        required
                        value={model.passwordForm.newPassword}
                        onChange={(event) => {
                          model.passwordForm.newPassword = event.target.value;
                        }}
                      />
                    </div>
                    <div className="parameter-row">
                      <label htmlFor="settings-confirmPassword">
                        {t("m.6fde05a916c6")}
                      </label>
                      <SettingsPassword
                        id="settings-confirmPassword"
                        name="confirmPassword"
                        autoComplete="new-password"
                        required
                        value={model.passwordForm.confirmPassword}
                        onChange={(event) => {
                          model.passwordForm.confirmPassword =
                            event.target.value;
                        }}
                      />
                    </div>
                  </SettingsGroup>
                  <div className="form-actions">
                    <Button
                      type="submit"
                      className="primary-button"
                      disabled={model.busy.value}
                    >
                      {t("m.828170843089")}
                    </Button>
                  </div>
                </form>
              </section>
              <section
                className="workspace-section settings-section session-settings"
                aria-labelledby="sessions-heading"
              >
                <div className="workspace-section-heading">
                  <h2 id="sessions-heading">{t("m.9fd94953b94b")}</h2>
                  <div className="workspace-actions">
                    <span className="session-count">
                      {t("session.count", { "0": model.sessions.value.length })}
                    </span>{" "}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => void refreshSessions()}
                      disabled={model.busy.value || sessionsRefreshing.value}
                    >
                      {t("settings.refreshSessions")}
                    </Button>
                  </div>
                </div>
                <div className="settings-list-body">
                  {sessionsError.value ? (
                    <Alert
                      role="alert"
                      tone="danger"
                      title={t("management.error")}
                    >
                      {sessionsError.value}
                    </Alert>
                  ) : null}

                  {model.sessions.value.length ? (
                    <SettingsGroup className="settings-record-list">
                      {model.sessions.value.map((session) => {
                        const device = describeSessionDevice(session.device);
                        const deviceLabel = [
                          t(`session.device.${device.kind}`),
                          device.os,
                          device.browser,
                        ]
                          .filter(Boolean)
                          .join(" / ");
                        return (
                          <article
                            className="settings-record-row session-item"
                            key={session.id}
                          >
                            <div className="settings-record-info session-info">
                              <strong>
                                {[
                                  t(`session.device.${device.kind}`),
                                  device.os,
                                  device.browser,
                                ]
                                  .filter(Boolean)
                                  .join(" / ")}
                              </strong>
                              <span>
                                {session.remoteAddr || t("m.f4a131451fce")}
                              </span>
                              <small>
                                {t("session.lastActiveAt", {
                                  "0": model.formatTime(session.lastSeenAt),
                                })}
                              </small>
                            </div>
                            <Button
                              variant="ghost"
                              disabled={model.busy.value || session.current}
                              aria-label={
                                session.current
                                  ? t("m.62a9388a42a8")
                                  : t("m.6cfb5f2a5be8", { "0": deviceLabel })
                              }
                              onClick={() => void model.revokeSession(session)}
                            >
                              {session.current
                                ? t("m.cb62ebd689ee")
                                : t("m.926a50b98ece")}
                            </Button>
                          </article>
                        );
                      })}
                    </SettingsGroup>
                  ) : (
                    <p>{t("m.214dc45ab589")}</p>
                  )}
                </div>
              </section>
              {admin ? (
                <MonitoringSettings key={model.currentUser.value!.id} />
              ) : null}
              {admin ? (
                <AccountManagement currentId={model.currentUser.value!.id} />
              ) : null}
            </div>
            {admin ? (
              <section
                className="workspace-section settings-section guest-settings publication-settings"
                aria-labelledby="publication-heading"
              >
                <header className="workspace-section-heading">
                  <div>
                    <h2 id="publication-heading">
                      {t("settings.publicAccess")}
                    </h2>
                  </div>
                  <div className="workspace-actions">
                    {" "}
                    <Button
                      variant="ghost"
                      onClick={() => void publication.load()}
                      disabled={publication.busy.value}
                    >
                      {t("guest.reload")}
                    </Button>
                  </div>
                </header>
                <div className="publication-body">
                  {publication.error.value ? (
                    <Alert
                      role="alert"
                      tone="danger"
                      title={t("management.error")}
                    >
                      {publication.error.value}
                    </Alert>
                  ) : null}
                  {publication.notice.value ? (
                    <Alert role="status" title={t("management.notice")}>
                      {publication.notice.value}
                    </Alert>
                  ) : null}
                  {policy && publication.options.value ? (
                    <form
                      className="publication-form"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void publication.save();
                      }}
                    >
                      <fieldset disabled={publication.busy.value}>
                        <SettingsGroup className="publication-policy">
                          <label className="guest-check">
                            <Checkbox
                              checked={policy.enabled}
                              onCheckedChange={(value) => {
                                policy.enabled = value === true;
                              }}
                            />
                            {t("guest.enabled")}
                          </label>
                          <div className="parameter-row">
                            <label htmlFor="personalsettings-field-2">
                              {t("guest.title")}
                            </label>
                            <Input
                              id="personalsettings-field-2"
                              required
                              value={policy.title}
                              onChange={(event) => {
                                policy.title = event.target.value;
                              }}
                            />
                          </div>
                          <label className="guest-check">
                            <Checkbox
                              checked={policy.showStatus}
                              onCheckedChange={(value) => {
                                policy.showStatus = value === true;
                              }}
                            />
                            {t("guest.showStatus")}
                          </label>
                          <MultiSelect
                            className="parameter-row publication-metric-field"
                            label={t("guest.metrics")}
                            placeholder={t("guest.selectMetrics")}
                            options={publication.options.value.metrics.map(
                              (metric) => ({
                                value: metric.key,
                                label: t(`guest.metric.${metric.key}`),
                              }),
                            )}
                            value={policy.metrics}
                            maxVisible={1}
                            disabled={publication.busy.value}
                            onValueChange={(value) => {
                              policy.metrics = value;
                            }}
                          />
                          <div className="parameter-row">
                            <span>{t("guest.history")}</span>
                            <Select
                              value={String(policy.historyHours)}
                              onValueChange={(value) => {
                                policy.historyHours = Number(value);
                              }}
                              options={publication.options.value.historyHours.map(
                                (hours) => ({
                                  value: String(hours),
                                  label:
                                    hours === 0
                                      ? t("guest.historyOff")
                                      : hours + " " + t("guest.hours"),
                                }),
                              )}
                            />
                          </div>
                        </SettingsGroup>
                        <div className="publication-resources">
                          <div>
                            <h3>{t("settings.publishedResources")}</h3>
                          </div>
                          {resources.length ? (
                            <SettingsGroup className="publication-resource-list">
                              {resources.map((resource) => {
                                const selected = publication.selected(
                                  resource.kind,
                                  resource.id,
                                );
                                return (
                                  <div
                                    className="guest-resource publication-resource"
                                    key={resource.kind + resource.id}
                                  >
                                    <div className="publication-resource-heading">
                                      <label className="guest-check">
                                        <Checkbox
                                          checked={!!selected}
                                          onCheckedChange={(value) =>
                                            publication.toggle(
                                              resource.kind,
                                              resource.id,
                                              value === true,
                                            )
                                          }
                                        />
                                        <strong>{resource.label}</strong>
                                      </label>
                                      <span>
                                        {t(
                                          resource.kind === "nodes"
                                            ? "guest.nodes"
                                            : "guest.services",
                                        )}
                                      </span>
                                    </div>
                                    {selected ? (
                                      <Input
                                        id={undefined}
                                        aria-label={t("guest.publicName")}
                                        placeholder={t("guest.publicName")}
                                        required
                                        value={selected.name}
                                        onChange={(event) =>
                                          publication.name(
                                            resource.kind,
                                            resource.id,
                                            event.target.value,
                                          )
                                        }
                                      />
                                    ) : null}
                                  </div>
                                );
                              })}
                            </SettingsGroup>
                          ) : (
                            <p className="publication-empty">
                              {t("settings.noPublishableResources")}
                            </p>
                          )}
                        </div>
                        <div className="publication-actions">
                          <Button type="submit" className="primary-button">
                            {t("guest.save")}
                          </Button>
                          <Button
                            type="button"
                            variant="secondary"
                            onClick={() => void publication.inspect()}
                          >
                            {t("guest.preview")}
                          </Button>
                        </div>
                      </fieldset>
                    </form>
                  ) : !publication.error.value ? (
                    <p role="status">{t("load.initial")}</p>
                  ) : null}
                  {publication.preview.value ? (
                    <div className="publication-preview">
                      <GuestDisplay preview={publication.preview.value} />
                    </div>
                  ) : null}
                </div>
              </section>
            ) : null}
          </ManagementPage>
        );
      };
    },
    input,
    ["model"],
    [],
    {},
    true,
  );
}
