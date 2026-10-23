import { api, operationRequest, type AgentNode } from "../api";
import type { GuestDisplay, GuestOptions, GuestSettings } from "../guest";
import { t } from "../i18n";
import { exactJSON } from "../operations";
import { afterMount, cell, derived } from "../state";
import GuestDisplayView from "./GuestDisplay";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./GuestSettings.css";
import {
  ManagementPage,
  Button,
  Input,
  Textarea,
  Checkbox,
  Select,
  Card,
  Badge,
  Alert,
  Tabs,
  TabsList,
  TabsTrigger,
  checkboxProps,
  selectOptions,
} from "./ManagementUI";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function GuestSettings(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const options = cell<GuestOptions>(),
        settings = cell<GuestSettings>(),
        nodes = cell<AgentNode[]>([]),
        probes = cell<
          Array<{
            id: string;
            name: string;
          }>
        >([]),
        preview = cell<GuestDisplay>(),
        busy = cell(false),
        error = cell(""),
        notice = cell("");
      const nodeChoices = derived(() => [
        ...nodes.value,
        ...(settings.value?.nodes ?? [])
          .filter((r) => !nodes.value.some((n) => n.nodeId === r.id))
          .map((r) => ({
            nodeId: r.id,
            displayName: t("guest.missingResource", {
              name: r.name,
            }),
          })),
      ]);
      const probeChoices = derived(() => [
        ...probes.value,
        ...(settings.value?.probes ?? [])
          .filter((r) => !probes.value.some((p) => p.id === r.id))
          .map((r) => ({
            id: r.id,
            name: t("guest.missingResource", {
              name: r.name,
            }),
          })),
      ]);
      function selected(kind: "nodes" | "probes", id: string) {
        return settings.value?.[kind].find((r) => r.id === id);
      }
      function toggle(kind: "nodes" | "probes", id: string, checked: boolean) {
        if (!settings.value) return;
        settings.value[kind] = settings.value[kind].filter((r) => r.id !== id);
        if (checked)
          settings.value[kind].push({
            id,
            name: "",
          });
        preview.value = undefined;
      }
      function name(kind: "nodes" | "probes", id: string, value: string) {
        const resource = selected(kind, id);
        if (resource) resource.name = value;
        preview.value = undefined;
      }
      async function load() {
        busy.value = true;
        error.value = "";
        try {
          const [config, list, services] = await Promise.all([
            operationRequest<GuestOptions>("guest.settings"),
            api.nodes(),
            operationRequest<{
              probes: Array<{
                id: string;
                name: string;
              }>;
            }>("probes.list"),
          ]);
          options.value = config;
          settings.value = config.settings;
          nodes.value = list.nodes.filter(
            (n) => !["pending", "revoked"].includes(n.status),
          );
          probes.value = services.probes;
        } catch (e) {
          error.value = String(e);
        } finally {
          busy.value = false;
        }
      }
      async function save() {
        if (!settings.value) return;
        busy.value = true;
        error.value = "";
        notice.value = "";
        preview.value = undefined;
        try {
          settings.value = await operationRequest<GuestSettings>(
            "guest.configure",
            {},
            {
              body: exactJSON(settings.value),
            },
          );
          notice.value = t("guest.saved");
        } catch (e) {
          error.value = String(e);
        } finally {
          busy.value = false;
        }
      }
      async function inspect() {
        busy.value = true;
        error.value = "";
        try {
          preview.value = await operationRequest<GuestDisplay>("guest.preview");
        } catch (e) {
          error.value = String(e);
        } finally {
          busy.value = false;
        }
      }
      afterMount(load);
      return () => (
        <>
          <ManagementPage
            className={__classes("guest-settings workspace-page")}
            data-v-85553d14=""
          >
            <header data-v-85553d14="">
              <h1 data-v-85553d14="">{t("guest.settings")}</h1>
              <p className={__classes("muted")} data-v-85553d14="">
                {t("guest.settingsHint")}
              </p>
            </header>
            {error.value ? (
              <Alert
                role="alert"
                className={__classes("guest-error")}
                data-v-85553d14=""
                tone="danger"
                title={t("management.error")}
              >
                {error.value}
              </Alert>
            ) : null}
            {notice.value ? (
              <Alert
                role="status"
                data-v-85553d14=""
                tone="info"
                title={t("management.notice")}
              >
                {notice.value}
              </Alert>
            ) : null}
            {settings.value && options.value ? (
              <form
                onSubmit={(event: any) => {
                  event.preventDefault();
                  __call(save, event);
                }}
                data-v-85553d14=""
              >
                <fieldset disabled={busy.value as any} data-v-85553d14="">
                  <Card
                    className={__classes("guest-settings-card management-card")}
                    data-v-85553d14=""
                    title={t("management.publication")}
                  >
                    <label
                      className={__classes("guest-check")}
                      data-v-85553d14=""
                    >
                      <Checkbox
                        {...checkboxProps(
                          settings.value!.enabled,
                          (__nextValue: any) => {
                            settings.value!.enabled = __nextValue;
                          },
                          undefined,
                        )}
                        data-v-85553d14=""
                      ></Checkbox>
                      {t("guest.enabled")}
                    </label>
                    <label data-v-85553d14="">
                      {t("guest.title")}
                      <Input
                        required={true}
                        {...__input(
                          settings.value!.title,
                          (__nextValue: any) => {
                            settings.value!.title = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-85553d14=""
                      ></Input>
                    </label>
                    <label
                      className={__classes("guest-check")}
                      data-v-85553d14=""
                    >
                      <Checkbox
                        {...checkboxProps(
                          settings.value!.showStatus,
                          (__nextValue: any) => {
                            settings.value!.showStatus = __nextValue;
                          },
                          undefined,
                        )}
                        data-v-85553d14=""
                      ></Checkbox>
                      {t("guest.showStatus")}
                    </label>

                    <h3>{t("guest.metrics")}</h3>
                    <Fragment>
                      {__each(options.value.metrics, (metric) => (
                        <label
                          key={metric.key as any}
                          className={__classes("guest-check")}
                          data-v-85553d14=""
                        >
                          <Checkbox
                            value={metric.key as any}
                            {...checkboxProps(
                              settings.value!.metrics,
                              (__nextValue: any) => {
                                settings.value!.metrics = __nextValue;
                              },
                              metric.key,
                            )}
                            data-v-85553d14=""
                          ></Checkbox>
                          {t(`guest.metric.${metric.key}`)}
                        </label>
                      ))}
                    </Fragment>
                    <label data-v-85553d14="">
                      {t("guest.history")}
                      <Select
                        value={String(settings.value!.historyHours ?? "")}
                        onValueChange={(next) => {
                          const __nextValue: any =
                            typeof settings.value!.historyHours === "number"
                              ? Number(next)
                              : next;
                          settings.value!.historyHours = __nextValue;
                        }}
                        data-v-85553d14=""
                        options={selectOptions(
                          <>
                            <Fragment>
                              {__each(options.value.historyHours, (hours) => (
                                <option
                                  key={hours as any}
                                  value={hours as any}
                                  data-v-85553d14=""
                                >
                                  {hours === 0
                                    ? t("guest.historyOff")
                                    : hours + " " + t("guest.hours")}
                                </option>
                              ))}
                            </Fragment>
                          </>,
                        )}
                      ></Select>
                    </label>
                  </Card>
                  <Card
                    className={__classes("guest-settings-card management-card")}
                    data-v-85553d14=""
                    title={<>{t("guest.nodes")}</>}
                  >
                    <p className={__classes("muted")} data-v-85553d14="">
                      {t("guest.namesHint")}
                    </p>
                    <Fragment>
                      {__each(nodeChoices.value, (node) => (
                        <div
                          key={node.nodeId as any}
                          className={__classes("guest-resource")}
                          data-v-85553d14=""
                        >
                          <label
                            className={__classes("guest-check")}
                            data-v-85553d14=""
                          >
                            <Checkbox
                              checked={!!selected("nodes", node.nodeId) as any}
                              onCheckedChange={(checked) => {
                                toggle("nodes", node.nodeId, checked === true);
                              }}
                              data-v-85553d14=""
                            ></Checkbox>
                            {node.displayName}
                          </label>
                          {selected("nodes", node.nodeId) ? (
                            <label data-v-85553d14="">
                              {t("guest.publicName")}
                              <Input
                                value={
                                  selected("nodes", node.nodeId)?.name as any
                                }
                                required={true}
                                onInput={(event: any) => {
                                  name(
                                    "nodes",
                                    node.nodeId,
                                    (event.target as HTMLInputElement).value,
                                  );
                                }}
                                data-v-85553d14=""
                              ></Input>
                            </label>
                          ) : null}
                        </div>
                      ))}
                    </Fragment>
                    {!nodeChoices.value.length ? (
                      <p className={__classes("muted")} data-v-85553d14="">
                        {t("guest.noNodes")}
                      </p>
                    ) : null}
                  </Card>
                  <Card
                    className={__classes("guest-settings-card management-card")}
                    data-v-85553d14=""
                    title={<>{t("guest.services")}</>}
                  >
                    <Fragment>
                      {__each(probeChoices.value, (probe) => (
                        <div
                          key={probe.id as any}
                          className={__classes("guest-resource")}
                          data-v-85553d14=""
                        >
                          <label
                            className={__classes("guest-check")}
                            data-v-85553d14=""
                          >
                            <Checkbox
                              checked={!!selected("probes", probe.id) as any}
                              onCheckedChange={(checked) => {
                                toggle("probes", probe.id, checked === true);
                              }}
                              data-v-85553d14=""
                            ></Checkbox>
                            {probe.name}
                          </label>
                          {selected("probes", probe.id) ? (
                            <label data-v-85553d14="">
                              {t("guest.publicName")}
                              <Input
                                value={
                                  selected("probes", probe.id)?.name as any
                                }
                                required={true}
                                onInput={(event: any) => {
                                  name(
                                    "probes",
                                    probe.id,
                                    (event.target as HTMLInputElement).value,
                                  );
                                }}
                                data-v-85553d14=""
                              ></Input>
                            </label>
                          ) : null}
                        </div>
                      ))}
                    </Fragment>
                    {!probeChoices.value.length ? (
                      <p className={__classes("muted")} data-v-85553d14="">
                        {t("guest.noProbes")}
                      </p>
                    ) : null}
                  </Card>
                  <div
                    className={__classes("guest-settings-actions")}
                    data-v-85553d14=""
                  >
                    <Button
                      type="submit"
                      className={__classes("primary-button")}
                      data-v-85553d14=""
                      variant="primary"
                    >
                      {t("guest.save")}
                    </Button>
                    <Button
                      type="button"
                      onClick={(event: any) => {
                        __call(inspect, event);
                      }}
                      data-v-85553d14=""
                      variant="secondary"
                    >
                      {t("guest.preview")}
                    </Button>
                    <Button
                      type="button"
                      onClick={(event: any) => {
                        __call(load, event);
                      }}
                      data-v-85553d14=""
                      variant="secondary"
                    >
                      {t("guest.reload")}
                    </Button>
                  </div>
                </fieldset>
              </form>
            ) : null}
            {preview.value ? (
              <GuestDisplayView
                preview={preview.value as any}
                data-v-85553d14=""
              ></GuestDisplayView>
            ) : null}
          </ManagementPage>
        </>
      );
    },
    input,
    [],
    [],
    {},
    true,
  );
}
