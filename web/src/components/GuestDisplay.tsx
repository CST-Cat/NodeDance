import { Fragment } from "react";
import { ApiError, operationRequest } from "../api";
import type { GuestDisplay } from "../guest";
import { t } from "../i18n";
import {
  __call,
  __classes,
  __each,
  __input,
  afterMount,
  beforeDispose,
  cell,
  derived,
  lazyView,
  useController,
} from "../state";
import "./GuestDisplay.css";

const HistoricalMetrics = lazyView(() => import("./HistoricalMetrics"));
type Props = {
  preview?: GuestDisplay;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function GuestDisplay(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const snapshot = cell<GuestDisplay>(),
        error = cell(""),
        loading = cell(false),
        filter = cell("");
      const data = derived(() => props.preview ?? snapshot.value);
      const nodes = derived(
        () =>
          data.value?.nodes.filter((n) =>
            n.name.toLowerCase().includes(filter.value.toLowerCase()),
          ) ?? [],
      );
      let timer: ReturnType<typeof setTimeout> | undefined,
        controller: AbortController | undefined;
      async function load() {
        if (loading.value) return;
        loading.value = true;
        error.value = "";
        const request = new AbortController();
        controller = request;
        try {
          const result = await operationRequest<GuestDisplay>(
            "guest.display",
            {},
            {
              signal: request.signal,
            },
          );
          if (!request.signal.aborted) snapshot.value = result;
        } catch (reason) {
          if (!request.signal.aborted) {
            snapshot.value = undefined;
            error.value =
              reason instanceof ApiError && reason.status === 404
                ? t("guest.unavailable")
                : reason instanceof Error
                  ? reason.message
                  : String(reason);
          }
        } finally {
          if (!request.signal.aborted) {
            loading.value = false;
            if (!props.preview) timer = setTimeout(load, (snapshot.value?.refreshSeconds ?? 30) * 1000);
          }
        }
      }
      function refresh() {
        if (timer) clearTimeout(timer);
        void load();
      }
      function value(v: number | undefined, unit: string) {
        if (v === undefined) return "—";
        if (unit === "B/s") return (v / 1048576).toFixed(2) + " MiB/s";
        if (unit === "s") return (v / 86400).toFixed(1) + " " + t("guest.days");
        return v.toFixed(1) + " " + unit;
      }
      afterMount(() => {
        if (!props.preview) void load();
      });
      beforeDispose(() => {
        controller?.abort();
        if (timer) clearTimeout(timer);
      });
      return () => (
        <>
          <section
            data-testid="guest-display"
            className={__classes("guest-page")}
            data-v-1486f506=""
          >
            <header data-v-1486f506="">
              <div data-v-1486f506="">
                <p className={__classes("eyebrow")} data-v-1486f506="">
                  {t("guest.mode")}
                </p>
                <h1 data-v-1486f506="">
                  {data.value?.title ?? t("guest.mode")}
                </h1>
                <p className={__classes("muted")} data-v-1486f506="">
                  {t("guest.readOnly")}
                </p>
              </div>
              <div className={__classes("guest-actions")} data-v-1486f506="">
                {!__props.preview ? (
                  <button
                    type="button"
                    onClick={(event: any) => {
                      emit("back");
                    }}
                    data-v-1486f506=""
                  >
                    {t("guest.back")}
                  </button>
                ) : null}
                {!__props.preview ? (
                  <button
                    type="button"
                    disabled={loading.value as any}
                    onClick={(event: any) => {
                      __call(refresh, event);
                    }}
                    data-v-1486f506=""
                  >
                    {t("guest.refresh")}
                  </button>
                ) : null}
              </div>
            </header>
            {error.value ? (
              <p
                role="alert"
                className={__classes("guest-error")}
                data-v-1486f506=""
              >
                {error.value}
              </p>
            ) : null}
            {loading.value && !data.value ? (
              <p role="status" data-v-1486f506="">
                {t("guest.loading")}
              </p>
            ) : null}
            {data.value ? (
              <Fragment>
                <p className={__classes("muted")} data-v-1486f506="">
                  {t("guest.updated")}{" "}
                  {new Date(data.value.generatedAt).toLocaleString()}
                </p>
                {data.value.nodes.length ? (
                  <label
                    className={__classes("guest-search")}
                    data-v-1486f506=""
                  >
                    {t("guest.search")}
                    <input
                      type="search"
                      {...__input(
                        filter.value,
                        (__nextValue: any) => {
                          filter.value = __nextValue;
                        },
                        "search",
                        undefined,
                        [],
                      )}
                      data-v-1486f506=""
                    ></input>
                  </label>
                ) : null}
                <div className={__classes("guest-grid")} data-v-1486f506="">
                  <Fragment>
                    {__each(nodes.value, (node) => (
                      <article
                        key={node.id as any}
                        className={__classes("guest-card")}
                        data-v-1486f506=""
                      >
                        <h2 data-v-1486f506="">{node.name}</h2>
                        {node.status ? (
                          <p
                            className={__classes("guest-state")}
                            data-v-1486f506=""
                          >
                            {t(`guest.state.${node.status}`)}
                          </p>
                        ) : null}
                        <dl data-v-1486f506="">
                          <Fragment>
                            {__each(node.metrics, (metric, key) => (
                              <div key={key as any} data-v-1486f506="">
                                <dt data-v-1486f506="">
                                  {t(`guest.metric.${key}`)}
                                </dt>
                                <dd data-v-1486f506="">
                                  {metric.status === "known"
                                    ? value(metric.value, metric.unit)
                                    : "—"}
                                  {metric.status !== "known" ? (
                                    <small data-v-1486f506="">
                                      {t(`guest.state.${metric.status}`)}
                                    </small>
                                  ) : null}
                                </dd>
                              </div>
                            ))}
                          </Fragment>
                        </dl>
                        {node.history ? (
                          <HistoricalMetrics
                            history={
                              {
                                ...node.history,
                                nodeId: node.id,
                              } as any
                            }
                            loading={false as any}
                            error=""
                            nodeOnline={node.status !== ("offline" as any)}
                            availableOnly={true as any}
                            data-v-1486f506=""
                          ></HistoricalMetrics>
                        ) : null}
                      </article>
                    ))}
                  </Fragment>
                </div>
                {data.value.probes.length ? (
                  <section
                    className={__classes("guest-services")}
                    data-v-1486f506=""
                  >
                    <h2 data-v-1486f506="">{t("guest.services")}</h2>
                    <div className={__classes("guest-grid")} data-v-1486f506="">
                      <Fragment>
                        {__each(data.value.probes, (probe) => (
                          <article
                            key={probe.id as any}
                            className={__classes("guest-card")}
                            data-v-1486f506=""
                          >
                            <h3 data-v-1486f506="">{probe.name}</h3>
                            <p data-v-1486f506="">
                              {t(`guest.state.${probe.status}`)}
                            </p>
                            {probe.latencyMs !== undefined ? (
                              <p data-v-1486f506="">
                                {probe.latencyMs}
                                {" ms"}
                              </p>
                            ) : null}
                            {probe.checkedAt ? (
                              <p
                                className={__classes("muted")}
                                data-v-1486f506=""
                              >
                                {new Date(probe.checkedAt).toLocaleString()}
                              </p>
                            ) : null}
                          </article>
                        ))}
                      </Fragment>
                    </div>
                  </section>
                ) : null}
                {!data.value.nodes.length && !data.value.probes.length ? (
                  <p className={__classes("muted")} data-v-1486f506="">
                    {t("guest.empty")}
                  </p>
                ) : null}
              </Fragment>
            ) : null}
          </section>
        </>
      );
    },
    input,
    ["preview"],
    [],
    {},
    true,
  );
}
