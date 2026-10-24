import { ExpandedTextarea } from "./ExpandedTextarea";
import { api, type DashboardPreference, type NodeLocation } from "../api";
import { display, locale, t } from "../i18n";
import {
  afterMount,
  cell,
  observable,
  watchState,
  useController,
} from "../state";
import {
  ManagementPage,
  Input,
  Checkbox,
  Select,
  Button,
} from "./ManagementUI";
import { resolveOperation } from "../operations";
import { serviceLogos } from "../service-logos";
import { systemLogos } from "../system-logo";
import CountryLogo from "./CountryLogo";
import NodeLogo from "./NodeLogo";
import LocationPicker from "./LocationPicker";
import "./PreferenceEditor.css";
type Props = {
  preference: DashboardPreference;
  location?: NodeLocation;
  title: string;
  platform?: string | null;
  os?: string | null;
  [key: string]: any;
};
export default function PreferenceEditor(input: Props) {
  return useController(
    (props: Props, emit: any) => {
      const form = observable<DashboardPreference>({ ...props.preference });
      const saving = cell(false),
        error = cell(""),
        notice = cell("");
      // Telemetry recreates parent props. Only changed persisted preference content
      // may replace the draft, never a new object holding the same saved values.
      watchState(
        () => JSON.stringify(props.preference),
        (value) => {
          Object.assign(form, JSON.parse(value));
        },
        { immediate: true },
      );
      const iconValues = cell<string[]>([]);
      const countryValues = cell<string[]>([]);
      const positionSchema = cell<any>({});
      afterMount(async () => {
        try {
          const { operation } = await resolveOperation(
            "nodes.save-preference",
            { params: { nodeId: props.preference.nodeId } },
          );
          positionSchema.value =
            operation.bodySchema?.properties?.location?.properties ?? {};
          countryValues.value = (
            operation.bodySchema?.properties?.countryCode?.enum ?? []
          ).map(String);
          iconValues.value = (
            operation.bodySchema?.properties?.icon?.enum ?? []
          ).map(String);
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : String(reason);
        }
      });
      function iconLabel(value: string) {
        if (!value)
          return t(
            form.targetKind === "node"
              ? "preferences.osDefault"
              : "m.844b8cc8dff7",
          );
        const labels: Record<string, string> = {
          server: "m.2388cbc6a486",
          globe: "m.fb8d739d08b9",
          database: "m.263200d7d529",
          shield: "m.afb63a620bdc",
          terminal: "m.e2a76ef1f12e",
          box: "m.6d23f04b2696",
          cloud: "m.32d42b97294a",
          folder: "m.39932f24fe11",
          activity: "m.fdc390061698",
        };
        return labels[value]
          ? t(labels[value])
          : (serviceLogos[value]?.name ??
              systemLogos[value as keyof typeof systemLogos]?.name ??
              value);
      }
      async function save() {
        error.value = "";
        notice.value = "";
        saving.value = true;
        try {
          const next = {
            ...form,
            countryCode: form.countryCode ?? "",
            location: form.location ?? null,
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
        <ManagementPage embedded className="preference-theme">
          <form
            className="preference-editor"
            onSubmit={(event) => {
              event.preventDefault();
              void save();
            }}
          >
            <header>
              <strong>{props.title}</strong>
              <small>{t("m.741981bd96aa")}</small>
            </header>
            <Input
              label={t("m.a98585871c53")}
              placeholder={t("m.0496871244c3")}
              value={form.alias}
              onChange={(event) => {
                form.alias = event.target.value;
              }}
            />
            <Select
              label={t("m.0d720eeea264")}
              value={form.icon}
              onValueChange={(value) => {
                form.icon = value;
              }}
              options={iconValues.value.map((value) => ({
                value,
                label: iconLabel(value),
                icon: (
                  <NodeLogo
                    icon={value || (form.targetKind === "node" ? "" : "box")}
                    platform={props.platform}
                    os={props.os}
                  />
                ),
              }))}
            />
            <div className="preference-wide">
              <ExpandedTextarea
                label={t("m.daede9881787")}
                rows={2}
                placeholder={t("m.9ad5244a4b67")}
                value={form.notes}
                onChange={(event) => {
                  form.notes = event.target.value;
                }}
              />
            </div>
            {form.targetKind === "node" ? (
              <>
                <Select
                  label={t("preferences.country")}
                  value={form.countryCode ?? ""}
                  onValueChange={(value) => {
                    form.countryCode = value;
                    form.location = null;
                  }}
                  options={countryValues.value
                    .map((value) => ({
                      value,
                      label: value
                        ? (new Intl.DisplayNames([locale.value], {
                            type: "region",
                          }).of(value) ?? value)
                        : t("preferences.countryAuto"),
                      icon: (
                        <CountryLogo
                          country={
                            value ? { status: "known", code: value } : undefined
                          }
                        />
                      ),
                    }))
                    .sort((a, b) =>
                      !a.value
                        ? -1
                        : !b.value
                          ? 1
                          : a.label.localeCompare(b.label, locale.value),
                    )}
                />
                <LocationPicker
                  resolved={props.location}
                  countryCode={form.countryCode ?? ""}
                  countries={countryValues.value}
                  schema={positionSchema.value}
                  value={form.location ?? null}
                  onChange={(point) => {
                    form.location = point;
                    if (point) form.countryCode = point.countryCode;
                    else form.countryCode = "";
                  }}
                />
                <Input
                  label={t("m.889b28078e5d")}
                  value={form.group}
                  placeholder={t("m.e86ea3f280d9")}
                  onChange={(event) => {
                    form.group = event.target.value;
                  }}
                />
                <Input
                  label={t("m.2290354d1f88")}
                  autoComplete="off"
                  placeholder={t("m.5f4aaae405f3")}
                  value={form.sshHost}
                  onChange={(event) => {
                    form.sshHost = event.target.value;
                  }}
                />
                <Input
                  label={t("m.1c22cc57aee8")}
                  type="number"
                  min={1}
                  max={65535}
                  step={1}
                  inputMode="numeric"
                  required
                  value={form.sshPort}
                  onChange={(event) => {
                    form.sshPort = Number(event.target.value);
                  }}
                />
                <Input
                  label={t("m.4249329ab914")}
                  autoComplete="off"
                  placeholder={t("m.c650b8295c49")}
                  value={form.sshUser}
                  onChange={(event) => {
                    form.sshUser = event.target.value;
                  }}
                />
                <small className="ssh-preference-note">
                  {t("m.a5d21b75a337")}
                </small>
                <Input
                  label={t("m.0f59d3742e06")}
                  type="number"
                  min={-1000000}
                  max={1000000}
                  step={1}
                  required
                  value={form.sortOrder}
                  onChange={(event) => {
                    form.sortOrder = Number(event.target.value);
                  }}
                />
              </>
            ) : null}
            <div className="preference-wide">
              <Input
                label={t("m.5d52d4478187")}
                type="url"
                placeholder="https://service.example.com"
                value={form.serviceUrl}
                onChange={(event) => {
                  form.serviceUrl = event.target.value;
                }}
              />
            </div>
            <Checkbox
              label={t("m.0264c56e530a")}
              checked={form.visible}
              onCheckedChange={(value) => {
                form.visible = value === true;
              }}
            />
            <Checkbox
              label={t("m.173f88d28ebf")}
              checked={form.pinned}
              onCheckedChange={(value) => {
                form.pinned = value === true;
              }}
            />
            <div className="preference-actions">
              <Button type="submit" disabled={saving.value}>
                {saving.value ? t("m.ff509c9ba052") : t("m.f96c9903175e")}
              </Button>
              {notice.value ? (
                <span role="status">{display(notice.value)}</span>
              ) : null}
              {error.value ? (
                <span role="alert">{display(error.value)}</span>
              ) : null}
            </div>
          </form>
        </ManagementPage>
      );
    },
    input,
    ["preference", "title", "platform", "os", "location"],
    [],
    {},
    true,
  );
}
