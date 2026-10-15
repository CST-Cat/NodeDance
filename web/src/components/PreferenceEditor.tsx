import { api, type DashboardPreference } from "../api";
import { display, t } from "../i18n";
import { cell, observable, watchState } from "../state";

import { __call, __classes, __input, useController } from "../state";
import "./PreferenceEditor.css";
import UiSelect from "./UiSelect";
type Props = {
  preference: DashboardPreference;
  title: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function PreferenceEditor(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const form = observable<DashboardPreference>({
        ...props.preference,
      });
      const saving = cell(false);
      const error = cell("");
      const notice = cell("");
      watchState(
        () => props.preference,
        (value) => Object.assign(form, value),
        {
          deep: true,
        },
      );
      async function save() {
        error.value = "";
        notice.value = "";
        saving.value = true;
        try {
          const next = {
            ...form,
            alias: form.alias.trim(),
            notes: form.notes.trim(),
            serviceUrl: form.serviceUrl.trim(),
            sshHost: form.sshHost.trim(),
            sshUser: form.sshUser.trim(),
            sshPort: Number(form.sshPort) || 22,
            group: form.group.trim(),
          };
          await api.saveNodePreference(next.nodeId, next);
          Object.assign(form, next);
          emit("saved", next);
          notice.value = t("m.2f1a6d364d44");
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.4c4b8b19ae6a");
        } finally {
          saving.value = false;
        }
      }
      return () => (
        <>
          <form
            className={__classes("preference-editor")}
            onSubmit={(event: any) => {
              event.preventDefault();
              __call(save, event);
            }}
            data-v-9a768bdb=""
          >
            <header data-v-9a768bdb="">
              <strong data-v-9a768bdb="">{__props.title}</strong>
              <small data-v-9a768bdb="">{t("m.741981bd96aa")}</small>
            </header>
            <label data-v-9a768bdb="">
              {t("m.a98585871c53")}
              <input
                maxLength={128}
                placeholder={t("m.0496871244c3") as any}
                {...__input(
                  form.alias,
                  (__nextValue: any) => {
                    form.alias = __nextValue;
                  },
                  "",
                  undefined,
                  [],
                )}
                data-v-9a768bdb=""
              ></input>
            </label>
            <label data-v-9a768bdb="">
              {t("m.0d720eeea264")}
              <UiSelect
                modelValue={form.icon}
                onUpdateModelValue={(__nextValue: any) => {
                  form.icon = __nextValue;
                }}
                modelModifiers={{}}
                data-v-9a768bdb=""
              >
                <option value="" data-v-9a768bdb="">
                  {t("m.844b8cc8dff7")}
                </option>
                <option value="server" data-v-9a768bdb="">
                  {t("m.2388cbc6a486")}
                </option>
                <option value="globe" data-v-9a768bdb="">
                  {t("m.fb8d739d08b9")}
                </option>
                <option value="database" data-v-9a768bdb="">
                  {t("m.263200d7d529")}
                </option>
                <option value="shield" data-v-9a768bdb="">
                  {t("m.afb63a620bdc")}
                </option>
                <option value="terminal" data-v-9a768bdb="">
                  {t("m.e2a76ef1f12e")}
                </option>
                <option value="box" data-v-9a768bdb="">
                  {t("m.6d23f04b2696")}
                </option>
                <option value="cloud" data-v-9a768bdb="">
                  {t("m.32d42b97294a")}
                </option>
                <option value="folder" data-v-9a768bdb="">
                  {t("m.39932f24fe11")}
                </option>
                <option value="activity" data-v-9a768bdb="">
                  {t("m.fdc390061698")}
                </option>
              </UiSelect>
            </label>
            <label data-v-9a768bdb="">
              {t("m.daede9881787")}
              <textarea
                maxLength={2048}
                rows={2}
                placeholder={t("m.9ad5244a4b67") as any}
                {...__input(
                  form.notes,
                  (__nextValue: any) => {
                    form.notes = __nextValue;
                  },
                  "",
                  undefined,
                  [],
                )}
                data-v-9a768bdb=""
              ></textarea>
            </label>
            {form.targetKind === "node" ? (
              <label data-v-9a768bdb="">
                {t("m.889b28078e5d")}
                <input
                  maxLength={128}
                  placeholder={t("m.e86ea3f280d9") as any}
                  {...__input(
                    form.group,
                    (__nextValue: any) => {
                      form.group = __nextValue;
                    },
                    "",
                    undefined,
                    [],
                  )}
                  data-v-9a768bdb=""
                ></input>
              </label>
            ) : null}
            {form.targetKind === "node" ? (
              <label data-v-9a768bdb="">
                {t("m.2290354d1f88")}
                <input
                  maxLength={255}
                  autoComplete="off"
                  placeholder={t("m.5f4aaae405f3") as any}
                  {...__input(
                    form.sshHost,
                    (__nextValue: any) => {
                      form.sshHost = __nextValue;
                    },
                    "",
                    undefined,
                    [],
                  )}
                  data-v-9a768bdb=""
                ></input>
              </label>
            ) : null}
            {form.targetKind === "node" ? (
              <label data-v-9a768bdb="">
                {t("m.1c22cc57aee8")}
                <input
                  type="number"
                  min="1"
                  max="65535"
                  step="1"
                  inputMode="numeric"
                  required={true}
                  {...__input(
                    form.sshPort,
                    (__nextValue: any) => {
                      form.sshPort = __nextValue;
                    },
                    "number",
                    undefined,
                    ["number"],
                  )}
                  data-v-9a768bdb=""
                ></input>
              </label>
            ) : null}
            {form.targetKind === "node" ? (
              <label data-v-9a768bdb="">
                {t("m.4249329ab914")}
                <input
                  maxLength={64}
                  autoComplete="off"
                  placeholder={t("m.c650b8295c49") as any}
                  {...__input(
                    form.sshUser,
                    (__nextValue: any) => {
                      form.sshUser = __nextValue;
                    },
                    "",
                    undefined,
                    [],
                  )}
                  data-v-9a768bdb=""
                ></input>
              </label>
            ) : null}
            {form.targetKind === "node" ? (
              <small
                className={__classes("ssh-preference-note")}
                data-v-9a768bdb=""
              >
                {t("m.a5d21b75a337")}
              </small>
            ) : null}
            {form.targetKind === "node" ? (
              <label data-v-9a768bdb="">
                {t("m.0f59d3742e06")}
                <input
                  type="number"
                  min="-1000000"
                  max="1000000"
                  step="1"
                  required={true}
                  {...__input(
                    form.sortOrder,
                    (__nextValue: any) => {
                      form.sortOrder = __nextValue;
                    },
                    "number",
                    undefined,
                    ["number"],
                  )}
                  data-v-9a768bdb=""
                ></input>
              </label>
            ) : null}
            <label data-v-9a768bdb="">
              {t("m.5d52d4478187")}
              <input
                type="url"
                maxLength={2048}
                placeholder="https://service.example.com"
                {...__input(
                  form.serviceUrl,
                  (__nextValue: any) => {
                    form.serviceUrl = __nextValue;
                  },
                  "url",
                  undefined,
                  [],
                )}
                data-v-9a768bdb=""
              ></input>
            </label>
            <label
              className={__classes("preference-checkbox")}
              data-v-9a768bdb=""
            >
              <input
                type="checkbox"
                {...__input(
                  form.visible,
                  (__nextValue: any) => {
                    form.visible = __nextValue;
                  },
                  "checkbox",
                  undefined,
                  [],
                )}
                data-v-9a768bdb=""
              ></input>
              {t("m.0264c56e530a")}
            </label>
            <label
              className={__classes("preference-checkbox")}
              data-v-9a768bdb=""
            >
              <input
                type="checkbox"
                {...__input(
                  form.pinned,
                  (__nextValue: any) => {
                    form.pinned = __nextValue;
                  },
                  "checkbox",
                  undefined,
                  [],
                )}
                data-v-9a768bdb=""
              ></input>
              {t("m.173f88d28ebf")}
            </label>
            <div className={__classes("preference-actions")} data-v-9a768bdb="">
              <button
                type="submit"
                disabled={saving.value as any}
                data-v-9a768bdb=""
              >
                {saving.value ? t("m.ff509c9ba052") : t("m.f96c9903175e")}
              </button>
              {notice.value ? (
                <span role="status" data-v-9a768bdb="">
                  {display(notice.value)}
                </span>
              ) : null}
              {error.value ? (
                <span role="alert" data-v-9a768bdb="">
                  {display(error.value)}
                </span>
              ) : null}
            </div>
          </form>
        </>
      );
    },
    input,
    ["preference", "title"],
    [],
    {},
    true,
  );
}
