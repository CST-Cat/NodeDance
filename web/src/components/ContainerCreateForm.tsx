import { ExpandedTextarea } from "./ExpandedTextarea";
import type {
  ContainerCreateMount,
  ContainerCreatePort,
  ContainerCreateSpec,
} from "../api";
import { display, t } from "../i18n";
import { cell } from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./ContainerCreateForm.css";
import UiSelect from "./UiSelect";
type Props = {
  disabled?: boolean;
  submitting?: boolean;
  error?: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ContainerCreateForm(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const image = cell("");
      const name = cell("");
      const commandText = cell("");
      const environmentText = cell("");
      const network = cell("");
      const restartPolicy = cell<ContainerCreateSpec["restartPolicy"]>("no");
      const restartRetries = cell(0);
      const ports = cell<ContainerCreatePort[]>([]);
      const mounts = cell<ContainerCreateMount[]>([]);
      function addPort() {
        ports.value = [
          ...ports.value,
          {
            containerPort: 0,
            protocol: "tcp",
            hostIp: "",
            hostPort: "",
          },
        ];
      }
      function removePort(index: number) {
        ports.value = ports.value.filter((_, current) => current !== index);
      }
      function addMount() {
        mounts.value = [
          ...mounts.value,
          {
            type: "bind",
            source: "",
            target: "",
            readOnly: false,
          },
        ];
      }
      function removeMount(index: number) {
        mounts.value = mounts.value.filter((_, current) => current !== index);
      }
      function submit() {
        const command = commandText.value
          .split(/\r?\n/)
          .filter((argument) => argument.length > 0);
        const environment = environmentText.value
          .split(/\r?\n/)
          .filter((entry) => entry.trim().length > 0);
        const configuredPorts: ContainerCreatePort[] = [];
        for (const port of ports.value) {
          if (port.containerPort === 0 && !port.hostIp && !port.hostPort)
            continue;
          configuredPorts.push({
            ...port,
            hostIp: port.hostIp || undefined,
            hostPort: port.hostPort || undefined,
          });
        }
        const configuredMounts: ContainerCreateMount[] = [];
        for (const mount of mounts.value) {
          if (!mount.source && !mount.target) continue;
          configuredMounts.push({
            ...mount,
            source: mount.source || undefined,
          });
        }
        const spec: ContainerCreateSpec = {
          image: image.value.trim(),
          name: name.value.trim(),
          command: command.length ? command : undefined,
          environment: environment.length ? environment : undefined,
          ports: configuredPorts.length ? configuredPorts : undefined,
          mounts: configuredMounts.length ? configuredMounts : undefined,
          network: network.value.trim() || undefined,
          restartPolicy: restartPolicy.value,
          restartRetries:
            restartPolicy.value === "on-failure"
              ? restartRetries.value
              : undefined,
        };
        emit("submit", spec);
      }
      return () => (
        <>
          <details
            data-testid="container-create-form"
            className={__classes("container-create-form")}
            data-v-3bc66e7f=""
          >
            <summary data-v-3bc66e7f="">{t("m.d1f7023b4f8d")}</summary>
            <form
              onSubmit={(event: any) => {
                event.preventDefault();
                __call(submit, event);
              }}
              data-v-3bc66e7f=""
            >
              <div className={__classes("create-form-grid")} data-v-3bc66e7f="">
                <label data-v-3bc66e7f="">
                  {t("m.176c09844e0f")}
                  <input
                    required={true}
                    placeholder={t("m.4204fc5cf592") as any}
                    {...__input(
                      image.value,
                      (__nextValue: any) => {
                        image.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-3bc66e7f=""
                  ></input>
                </label>
                <label data-v-3bc66e7f="">
                  {t("m.62621c43654a")}
                  <input
                    required={true}
                    placeholder={t("m.6ae23d2114e1") as any}
                    {...__input(
                      name.value,
                      (__nextValue: any) => {
                        name.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-3bc66e7f=""
                  ></input>
                </label>
                <label data-v-3bc66e7f="">
                  {t("m.97b31b5d63f5")}
                  <input
                    placeholder={t("m.7f630368cc55") as any}
                    {...__input(
                      network.value,
                      (__nextValue: any) => {
                        network.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-3bc66e7f=""
                  ></input>
                </label>
                <label data-v-3bc66e7f="">
                  {t("m.c6e4aade79e8")}
                  <UiSelect
                    modelValue={restartPolicy.value}
                    onUpdateModelValue={(__nextValue: any) => {
                      restartPolicy.value = __nextValue;
                    }}
                    modelModifiers={{}}
                    data-v-3bc66e7f=""
                  >
                    <option value="no" data-v-3bc66e7f="">
                      {t("m.4858fc1fce95")}
                    </option>
                    <option value="always" data-v-3bc66e7f="">
                      {t("m.938f259b93a3")}
                    </option>
                    <option value="unless-stopped" data-v-3bc66e7f="">
                      {t("m.4ac3e0ecef3c")}
                    </option>
                    <option value="on-failure" data-v-3bc66e7f="">
                      {t("m.787e9ce9e178")}
                    </option>
                  </UiSelect>
                </label>
                {restartPolicy.value === "on-failure" ? (
                  <label data-v-3bc66e7f="">
                    {t("m.696303d9c413")}
                    <input
                      type="number"
                      min="0"
                      max="1000"
                      {...__input(
                        restartRetries.value,
                        (__nextValue: any) => {
                          restartRetries.value = __nextValue;
                        },
                        "number",
                        undefined,
                        ["number"],
                      )}
                      data-v-3bc66e7f=""
                    ></input>
                  </label>
                ) : null}
                <label
                  className={__classes("create-form-wide")}
                  data-v-3bc66e7f=""
                >
                  {t("m.570674fea082")}
                  <ExpandedTextarea
                    rows={3}
                    placeholder={t("m.42dca5a99d0f") as any}
                    {...__input(
                      commandText.value,
                      (__nextValue: any) => {
                        commandText.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-3bc66e7f=""
                  ></ExpandedTextarea>
                  <small data-v-3bc66e7f="">{t("m.47c94fbef3fe")}</small>
                </label>
                <label
                  className={__classes("create-form-wide")}
                  data-v-3bc66e7f=""
                >
                  {t("m.ae27b474ea4d")}
                  <ExpandedTextarea
                    rows={3}
                    placeholder={t("m.555aa8a036b4") as any}
                    {...__input(
                      environmentText.value,
                      (__nextValue: any) => {
                        environmentText.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-3bc66e7f=""
                  ></ExpandedTextarea>
                </label>
              </div>
              <fieldset
                className={__classes("create-form-list")}
                data-v-3bc66e7f=""
              >
                <legend data-v-3bc66e7f="">{t("m.c263674112e4")}</legend>
                <Fragment>
                  {__each(ports.value, (port, index) => (
                    <div
                      key={index as any}
                      className={__classes("create-form-row")}
                      data-v-3bc66e7f=""
                    >
                      <label data-v-3bc66e7f="">
                        {t("m.9c9980ac48fe")}
                        <input
                          type="number"
                          min="1"
                          max="65535"
                          {...__input(
                            port.containerPort,
                            (__nextValue: any) => {
                              port.containerPort = __nextValue;
                            },
                            "number",
                            undefined,
                            ["number"],
                          )}
                          data-v-3bc66e7f=""
                        ></input>
                      </label>
                      <label data-v-3bc66e7f="">
                        {t("m.ab2f31f30acf")}
                        <UiSelect
                          modelValue={port.protocol}
                          onUpdateModelValue={(__nextValue: any) => {
                            port.protocol = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-3bc66e7f=""
                        >
                          <option value="tcp" data-v-3bc66e7f="">
                            {"TCP"}
                          </option>
                          <option value="udp" data-v-3bc66e7f="">
                            {"UDP"}
                          </option>
                        </UiSelect>
                      </label>
                      <label data-v-3bc66e7f="">
                        {t("m.2ffee39fb19a")}
                        <input
                          placeholder={t("m.19caa396b494") as any}
                          {...__input(
                            port.hostIp,
                            (__nextValue: any) => {
                              port.hostIp = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-3bc66e7f=""
                        ></input>
                      </label>
                      <label data-v-3bc66e7f="">
                        {t("m.f4a4267edc03")}
                        <input
                          inputMode="numeric"
                          placeholder={t("m.fdae3b9db051") as any}
                          {...__input(
                            port.hostPort,
                            (__nextValue: any) => {
                              port.hostPort = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-3bc66e7f=""
                        ></input>
                      </label>
                      <button
                        type="button"
                        className={__classes("container-action")}
                        onClick={(event: any) => {
                          removePort(index);
                        }}
                        data-v-3bc66e7f=""
                      >
                        {t("m.6135d4159e89")}
                      </button>
                    </div>
                  ))}
                </Fragment>
                <button
                  type="button"
                  className={__classes("container-action")}
                  onClick={(event: any) => {
                    __call(addPort, event);
                  }}
                  data-v-3bc66e7f=""
                >
                  {t("m.173c6155fb1a")}
                </button>
              </fieldset>
              <fieldset
                className={__classes("create-form-list")}
                data-v-3bc66e7f=""
              >
                <legend data-v-3bc66e7f="">{t("m.6fc9c71cb14f")}</legend>
                <Fragment>
                  {__each(mounts.value, (mount, index) => (
                    <div
                      key={index as any}
                      className={__classes("create-form-row create-mount-row")}
                      data-v-3bc66e7f=""
                    >
                      <label data-v-3bc66e7f="">
                        {t("m.ba40014ff496")}
                        <UiSelect
                          modelValue={mount.type}
                          onUpdateModelValue={(__nextValue: any) => {
                            mount.type = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-3bc66e7f=""
                        >
                          <option value="bind" data-v-3bc66e7f="">
                            {t("m.8b61a84a6fc9")}
                          </option>
                          <option value="volume" data-v-3bc66e7f="">
                            {t("m.22256deee0ac")}
                          </option>
                        </UiSelect>
                      </label>
                      <label data-v-3bc66e7f="">
                        {mount.type === "bind"
                          ? t("m.bb2ab0977e56")
                          : t("m.a8833f0d0a67")}{" "}
                        <input
                          placeholder={
                            mount.type === "bind"
                              ? "/srv/data"
                              : ("app-data" as any)
                          }
                          {...__input(
                            mount.source,
                            (__nextValue: any) => {
                              mount.source = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-3bc66e7f=""
                        ></input>
                      </label>
                      <label data-v-3bc66e7f="">
                        {t("m.52e49d585de1")}
                        <input
                          placeholder="/var/lib/app"
                          {...__input(
                            mount.target,
                            (__nextValue: any) => {
                              mount.target = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-3bc66e7f=""
                        ></input>
                      </label>
                      <label
                        className={__classes("create-readonly")}
                        data-v-3bc66e7f=""
                      >
                        <input
                          type="checkbox"
                          {...__input(
                            mount.readOnly,
                            (__nextValue: any) => {
                              mount.readOnly = __nextValue;
                            },
                            "checkbox",
                            undefined,
                            [],
                          )}
                          data-v-3bc66e7f=""
                        ></input>
                        {t("m.3b5ec3533b0e")}
                      </label>
                      <button
                        type="button"
                        className={__classes("container-action")}
                        onClick={(event: any) => {
                          removeMount(index);
                        }}
                        data-v-3bc66e7f=""
                      >
                        {t("m.6135d4159e89")}
                      </button>
                    </div>
                  ))}
                </Fragment>
                <button
                  type="button"
                  className={__classes("container-action")}
                  onClick={(event: any) => {
                    __call(addMount, event);
                  }}
                  data-v-3bc66e7f=""
                >
                  {t("m.78c8f87622ca")}
                </button>
              </fieldset>
              <p className={__classes("create-form-note")} data-v-3bc66e7f="">
                {t("m.4576641fd0a6")}
              </p>
              {props.error ? (
                <p
                  role="alert"
                  className={__classes("create-form-error")}
                  data-v-3bc66e7f=""
                >
                  {display(props.error)}
                </p>
              ) : null}
              <button
                type="submit"
                disabled={props.disabled || (props.submitting as any)}
                className={__classes("container-action create-submit")}
                data-v-3bc66e7f=""
              >
                {props.submitting ? t("m.18931020ad52") : t("m.cb34cd41fbba")}
              </button>
            </form>
          </details>
        </>
      );
    },
    input,
    ["disabled", "submitting", "error"],
    [],
    {},
    true,
  );
}
