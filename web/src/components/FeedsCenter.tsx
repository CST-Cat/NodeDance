import { operationRequest } from "../api";
import { display, t } from "../i18n";
import { afterMount, beforeDispose, cell, watchState } from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./FeedsCenter.css";
import UiSelect from "./UiSelect";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function FeedsCenter(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      interface Feed {
        id: string;
        name: string;
        url: string;
        intervalMinutes: number;
        enabled: boolean;
        keywords: string[];
        channelIds: string[];
        notificationMode: string;
        digestMinutes: number;
        revision: number;
        status: string;
        error: string;
        lastSuccess: string;
        nextCheck: string;
      }
      interface Item {
        id: string;
        title: string;
        summary: string;
        url: string;
        published?: string;
        firstSeen: string;
        read: boolean;
      }
      interface Delivery {
        id: string;
        channelId: string;
        status: string;
        attempts: number;
        error: string;
        created: string;
      }
      interface Channel {
        id: string;
        name: string;
        enabled: boolean;
      }
      interface Options {
        intervalMinutes: number[];
        defaultIntervalMinutes: number;
        minimumIntervalMinutes: number;
        maximumIntervalMinutes: number;
      }
      const feeds = cell<Feed[]>([]),
        items = cell<Item[]>([]),
        deliveries = cell<Delivery[]>([]),
        channels = cell<Channel[]>([]),
        options = cell<Options>();
      const selected = cell(""),
        editing = cell(""),
        draft = cell({
          name: "",
          url: "",
          intervalMinutes: 30,
          enabled: true,
          keywords: [] as string[],
          channelIds: [] as string[],
          notificationMode: "off",
          digestMinutes: 60,
          revision: 0,
        });
      const keywordText = cell(""),
        search = cell(""),
        unread = cell(false),
        offset = cell(0),
        error = cell(""),
        busy = cell(false),
        removeConfirmed = cell(false);
      let stopped = false,
        epoch = 0,
        timer: ReturnType<typeof setTimeout> | undefined;
      const statusKeys: Record<string, string> = {
        queued: "feeds.queued",
        running: "feeds.running",
        succeeded: "feeds.succeeded",
        failed: "feeds.failed",
        canceled: "feeds.canceled",
        inactive: "feeds.inactive",
        retry: "feeds.retrying",
        sending: "feeds.sending",
      };
      function statusLabel(value: string) {
        return statusKeys[value] ? t(statusKeys[value]!) : value;
      }
      function message(e: unknown) {
        return e instanceof Error ? e.message : String(e);
      }
      function reset() {
        editing.value = "";
        keywordText.value = "";
        draft.value = {
          name: "",
          url: "",
          intervalMinutes: options.value?.defaultIntervalMinutes ?? 30,
          enabled: true,
          keywords: [],
          channelIds: [],
          notificationMode: "off",
          digestMinutes: 60,
          revision: 0,
        };
      }
      function edit(feed: Feed) {
        editing.value = feed.id;
        draft.value = {
          name: feed.name,
          url: feed.url,
          intervalMinutes: feed.intervalMinutes,
          enabled: feed.enabled,
          keywords: [...feed.keywords],
          channelIds: [...feed.channelIds],
          notificationMode: feed.notificationMode,
          digestMinutes: feed.digestMinutes,
          revision: feed.revision,
        };
        keywordText.value = feed.keywords.join("\n");
      }
      async function reload() {
        const current = epoch;
        try {
          const result = await operationRequest<{
            subscriptions: Feed[];
          }>("feeds.list");
          if (stopped || current !== epoch) return;
          feeds.value = result.subscriptions;
          if (
            selected.value &&
            !feeds.value.some((f) => f.id === selected.value)
          ) {
            selected.value = "";
            items.value = [];
            deliveries.value = [];
          }
          if (!selected.value && feeds.value.length)
            selected.value = feeds.value[0]!.id;
        } catch (e) {
          if (!stopped && current === epoch) error.value = message(e);
        }
      }
      async function page() {
        if (!selected.value) return;
        const current = epoch,
          id = selected.value;
        try {
          const [entries, history] = await Promise.all([
            operationRequest<{
              items: Item[];
            }>("feeds.items", {
              params: {
                id,
              },
              query: {
                offset: String(offset.value),
                limit: "50",
                search: search.value,
                unread: String(unread.value),
              },
            }),
            operationRequest<{
              deliveries: Delivery[];
            }>("feeds.deliveries", {
              params: {
                id,
              },
            }),
          ]);
          if (stopped || current !== epoch || id !== selected.value) return;
          items.value = entries.items;
          deliveries.value = history.deliveries;
        } catch (e) {
          if (current === epoch && !stopped) error.value = message(e);
        }
      }
      async function poll() {
        await reload();
        await page();
        if (!stopped) timer = setTimeout(poll, 5000);
      }
      async function perform(call: () => Promise<unknown>) {
        if (busy.value) return;
        busy.value = true;
        epoch++;
        error.value = "";
        try {
          await call();
          await reload();
          await page();
        } catch (e) {
          error.value = message(e);
        } finally {
          busy.value = false;
        }
      }
      async function save() {
        await perform(async () => {
          const result = await operationRequest<Feed>(
            editing.value ? "feeds.update" : "feeds.create",
            editing.value
              ? {
                  params: {
                    id: editing.value,
                  },
                }
              : {},
            {
              body: JSON.stringify({
                ...draft.value,
                keywords: keywordText.value
                  .split("\n")
                  .map((v) => v.trim())
                  .filter(Boolean),
              }),
            },
          );
          selected.value = result.id;
          reset();
        });
      }
      async function action(feed: Feed, verb: string) {
        await perform(() =>
          operationRequest("feeds." + verb, {
            params: {
              id: feed.id,
            },
          }),
        );
      }
      async function toggle(feed: Feed) {
        await perform(() =>
          operationRequest(
            "feeds.update",
            {
              params: {
                id: feed.id,
              },
            },
            {
              body: JSON.stringify({
                name: feed.name,
                url: feed.url,
                intervalMinutes: feed.intervalMinutes,
                enabled: !feed.enabled,
                keywords: feed.keywords,
                channelIds: feed.channelIds,
                notificationMode: feed.notificationMode,
                digestMinutes: feed.digestMinutes,
                revision: feed.revision,
              }),
            },
          ),
        );
      }
      async function remove(feed: Feed) {
        await perform(async () => {
          await operationRequest(
            "feeds.delete",
            {
              params: {
                id: feed.id,
              },
            },
            {
              body: JSON.stringify({
                revision: feed.revision,
                confirm: true,
              }),
            },
          );
          removeConfirmed.value = false;
          if (editing.value === feed.id) reset();
        });
      }
      async function read(item: Item) {
        await perform(() =>
          operationRequest(
            "feeds.read",
            {
              params: {
                id: selected.value,
                itemId: item.id,
              },
            },
            {
              body: JSON.stringify({
                read: !item.read,
              }),
            },
          ),
        );
      }
      watchState([selected, unread], () => {
        epoch++;
        offset.value = 0;
        removeConfirmed.value = false;
        void page();
      });
      afterMount(async () => {
        try {
          options.value = await operationRequest<Options>("feeds.options");
          draft.value.intervalMinutes = options.value.defaultIntervalMinutes;
          channels.value = (
            await operationRequest<{
              channels: Channel[];
            }>("channels.list")
          ).channels;
        } catch (e) {
          error.value = message(e);
        }
        if (!stopped) void poll();
      });
      beforeDispose(() => {
        stopped = true;
        epoch++;
        if (timer) clearTimeout(timer);
      });
      return () => (
        <>
          <section
            data-testid="feeds-center"
            className={__classes("feeds-center workspace-page")}
            data-v-9b049af3=""
          >
            <header data-v-9b049af3="">
              <p className={__classes("eyebrow")} data-v-9b049af3="">
                {"NODEDANCE"}
              </p>
              <h1 data-v-9b049af3="">{t("feeds.title")}</h1>
              <p className={__classes("muted")} data-v-9b049af3="">
                {t("feeds.description")}
              </p>
            </header>
            {error.value ? (
              <p
                role="alert"
                className={__classes("feed-error")}
                data-v-9b049af3=""
              >
                {display(error.value)}
              </p>
            ) : null}
            <form
              className={__classes("feed-card feed-form")}
              onSubmit={(event: any) => {
                event.preventDefault();
                __call(save, event);
              }}
              data-v-9b049af3=""
            >
              <h2 data-v-9b049af3="">
                {t(editing.value ? "feeds.edit" : "feeds.add")}
              </h2>
              <fieldset
                className={__classes(
                  "feed-settings-group feed-source-settings",
                )}
                data-v-9b049af3=""
              >
                <legend data-v-9b049af3="">{t("feeds.sourceSettings")}</legend>
                <label data-v-9b049af3="">
                  {t("feeds.name")}
                  <input
                    name="feedName"
                    required={true}
                    {...__input(
                      draft.value.name,
                      (__nextValue: any) => {
                        draft.value.name = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-9b049af3=""
                  ></input>
                </label>
                <label className={__classes("feed-url")} data-v-9b049af3="">
                  {t("feeds.url")}
                  <input
                    name="feedUrl"
                    type="url"
                    required={true}
                    {...__input(
                      draft.value.url,
                      (__nextValue: any) => {
                        draft.value.url = __nextValue;
                      },
                      "url",
                      undefined,
                      [],
                    )}
                    data-v-9b049af3=""
                  ></input>
                  <small className={__classes("muted")} data-v-9b049af3="">
                    {t("feeds.publicOnly")}
                  </small>
                </label>
                <label data-v-9b049af3="">
                  {t("feeds.interval")}
                  <span
                    className={__classes("feed-interval-controls")}
                    data-v-9b049af3=""
                  >
                    <UiSelect
                      name="feedInterval"
                      modelValue={draft.value.intervalMinutes}
                      onUpdateModelValue={(__nextValue: any) => {
                        draft.value.intervalMinutes = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-9b049af3=""
                    >
                      <Fragment>
                        {__each(options.value?.intervalMinutes, (minutes) => (
                          <option
                            key={minutes as any}
                            value={minutes as any}
                            data-v-9b049af3=""
                          >
                            {minutes} {t("feeds.minutes")}
                          </option>
                        ))}
                      </Fragment>
                      {options.value &&
                      !options.value.intervalMinutes.includes(
                        draft.value.intervalMinutes,
                      ) ? (
                        <option
                          value={draft.value.intervalMinutes as any}
                          data-v-9b049af3=""
                        >
                          {draft.value.intervalMinutes} {t("feeds.minutes")}
                        </option>
                      ) : null}
                    </UiSelect>
                    <input
                      name="feedCustomInterval"
                      type="number"
                      min={options.value?.minimumIntervalMinutes as any}
                      max={options.value?.maximumIntervalMinutes as any}
                      aria-label={t("feeds.customInterval") as any}
                      {...__input(
                        draft.value.intervalMinutes,
                        (__nextValue: any) => {
                          draft.value.intervalMinutes = __nextValue;
                        },
                        "number",
                        undefined,
                        ["number"],
                      )}
                      data-v-9b049af3=""
                    ></input>
                  </span>
                </label>
                <label
                  className={__classes("check feed-enabled")}
                  data-v-9b049af3=""
                >
                  <input
                    type="checkbox"
                    {...__input(
                      draft.value.enabled,
                      (__nextValue: any) => {
                        draft.value.enabled = __nextValue;
                      },
                      "checkbox",
                      undefined,
                      [],
                    )}
                    data-v-9b049af3=""
                  ></input>
                  {t("feeds.enabled")}
                </label>
              </fieldset>
              <fieldset
                className={__classes(
                  "feed-settings-group feed-notification-settings",
                )}
                data-v-9b049af3=""
              >
                <legend data-v-9b049af3="">{t("feeds.notifications")}</legend>
                <label data-v-9b049af3="">
                  {t("feeds.notifications")}
                  <UiSelect
                    name="feedNotificationMode"
                    modelValue={draft.value.notificationMode}
                    onUpdateModelValue={(__nextValue: any) => {
                      draft.value.notificationMode = __nextValue;
                    }}
                    modelModifiers={{}}
                    data-v-9b049af3=""
                  >
                    <option value="off" data-v-9b049af3="">
                      {t("feeds.off")}
                    </option>
                    <option value="immediate" data-v-9b049af3="">
                      {t("feeds.immediate")}
                    </option>
                    <option value="digest" data-v-9b049af3="">
                      {t("feeds.digest")}
                    </option>
                  </UiSelect>
                </label>
                <label
                  className={__classes("feed-keywords")}
                  data-v-9b049af3=""
                >
                  {t("feeds.keywords")}
                  <textarea
                    name="feedKeywords"
                    rows={3}
                    {...__input(
                      keywordText.value,
                      (__nextValue: any) => {
                        keywordText.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-9b049af3=""
                  ></textarea>
                  <small className={__classes("muted")} data-v-9b049af3="">
                    {t("feeds.keywordHint")}
                  </small>
                </label>
                {draft.value.notificationMode === "digest" ? (
                  <label data-v-9b049af3="">
                    {t("feeds.digestInterval")}
                    <input
                      type="number"
                      min={options.value?.minimumIntervalMinutes as any}
                      max={options.value?.maximumIntervalMinutes as any}
                      {...__input(
                        draft.value.digestMinutes,
                        (__nextValue: any) => {
                          draft.value.digestMinutes = __nextValue;
                        },
                        "number",
                        undefined,
                        ["number"],
                      )}
                      data-v-9b049af3=""
                    ></input>
                  </label>
                ) : null}
                {draft.value.notificationMode !== "off" ? (
                  <fieldset data-v-9b049af3="">
                    <legend data-v-9b049af3="">{t("feeds.channels")}</legend>
                    <Fragment>
                      {__each(
                        channels.value.filter((c) => c.enabled),
                        (channel) => (
                          <label
                            key={channel.id as any}
                            className={__classes("check")}
                            data-v-9b049af3=""
                          >
                            <input
                              type="checkbox"
                              value={channel.id as any}
                              {...__input(
                                draft.value.channelIds,
                                (__nextValue: any) => {
                                  draft.value.channelIds = __nextValue;
                                },
                                "checkbox",
                                channel.id,
                                [],
                              )}
                              data-v-9b049af3=""
                            ></input>
                            {channel.name}
                          </label>
                        ),
                      )}
                    </Fragment>
                    {!channels.value.some((c) => c.enabled) ? (
                      <p className={__classes("muted")} data-v-9b049af3="">
                        {t("feeds.noChannels")}
                      </p>
                    ) : null}
                  </fieldset>
                ) : null}
              </fieldset>
              <div className={__classes("feed-actions")} data-v-9b049af3="">
                <button
                  disabled={busy.value || (!options.value as any)}
                  className={__classes("primary-button")}
                  data-v-9b049af3=""
                >
                  {t("feeds.save")}
                </button>
                {editing.value ? (
                  <button
                    type="button"
                    className={__classes("secondary-button")}
                    onClick={(event: any) => {
                      __call(reset, event);
                    }}
                    data-v-9b049af3=""
                  >
                    {t("feeds.cancelEdit")}
                  </button>
                ) : null}
              </div>
            </form>
            <div className={__classes("feed-columns")} data-v-9b049af3="">
              <aside className={__classes("feed-card")} data-v-9b049af3="">
                <h2 data-v-9b049af3="">{t("feeds.subscriptions")}</h2>
                {!feeds.value.length ? (
                  <p className={__classes("muted")} data-v-9b049af3="">
                    {t("feeds.empty")}
                  </p>
                ) : null}
                <Fragment>
                  {__each(feeds.value, (feed) => (
                    <article
                      key={feed.id as any}
                      className={__classes("feed-subscription")}
                      data-v-9b049af3=""
                    >
                      <button
                        aria-pressed={selected.value === (feed.id as any)}
                        className={__classes("feed-select")}
                        onClick={(event: any) => {
                          selected.value = feed.id;
                        }}
                        data-v-9b049af3=""
                      >
                        {feed.name}
                      </button>
                      <p className={__classes("muted")} data-v-9b049af3="">
                        {feed.intervalMinutes} {t("feeds.minutes")}
                        {" · "}
                        {statusLabel(feed.status)}
                      </p>
                      {feed.error ? (
                        <p
                          className={__classes("feed-error")}
                          data-v-9b049af3=""
                        >
                          {display(feed.error)}
                        </p>
                      ) : null}
                      <p className={__classes("muted")} data-v-9b049af3="">
                        {t("feeds.lastSuccess")}
                        {": "}
                        {feed.lastSuccess.startsWith("0001")
                          ? "—"
                          : new Date(feed.lastSuccess).toLocaleString()}
                      </p>
                      <p className={__classes("muted")} data-v-9b049af3="">
                        {t("feeds.nextCheck")}
                        {": "}
                        {new Date(feed.nextCheck).toLocaleString()}
                      </p>
                      <div
                        className={__classes("feed-actions")}
                        data-v-9b049af3=""
                      >
                        <button
                          disabled={busy.value as any}
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            edit(feed);
                          }}
                          data-v-9b049af3=""
                        >
                          {t("feeds.edit")}
                        </button>
                        <button
                          disabled={busy.value || (!feed.enabled as any)}
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            action(feed, "refresh");
                          }}
                          data-v-9b049af3=""
                        >
                          {t("feeds.refresh")}
                        </button>
                        {["queued", "running"].includes(feed.status) ? (
                          <button
                            disabled={busy.value as any}
                            className={__classes("secondary-button")}
                            onClick={(event: any) => {
                              action(feed, "cancel");
                            }}
                            data-v-9b049af3=""
                          >
                            {t("feeds.cancelRefresh")}
                          </button>
                        ) : null}
                        <button
                          disabled={busy.value as any}
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            toggle(feed);
                          }}
                          data-v-9b049af3=""
                        >
                          {t(feed.enabled ? "feeds.disable" : "feeds.enable")}
                        </button>
                      </div>
                      {selected.value === feed.id ? (
                        <Fragment>
                          <label
                            className={__classes("check")}
                            data-v-9b049af3=""
                          >
                            <input
                              type="checkbox"
                              {...__input(
                                removeConfirmed.value,
                                (__nextValue: any) => {
                                  removeConfirmed.value = __nextValue;
                                },
                                "checkbox",
                                undefined,
                                [],
                              )}
                              data-v-9b049af3=""
                            ></input>
                            {t("feeds.deleteConfirm")}
                          </label>
                          <button
                            disabled={
                              busy.value || (!removeConfirmed.value as any)
                            }
                            className={__classes(
                              "secondary-button feed-delete",
                            )}
                            onClick={(event: any) => {
                              remove(feed);
                            }}
                            data-v-9b049af3=""
                          >
                            {t("feeds.delete")}
                          </button>
                        </Fragment>
                      ) : null}
                    </article>
                  ))}
                </Fragment>
              </aside>
              <div className={__classes("feed-card")} data-v-9b049af3="">
                <h2 data-v-9b049af3="">{t("feeds.entries")}</h2>
                <form
                  className={__classes("feed-actions")}
                  onSubmit={(event: any) => {
                    event.preventDefault();
                    offset.value = 0;
                    epoch++;
                    page();
                  }}
                  data-v-9b049af3=""
                >
                  <input
                    type="search"
                    aria-label={t("feeds.search") as any}
                    {...__input(
                      search.value,
                      (__nextValue: any) => {
                        search.value = __nextValue;
                      },
                      "search",
                      undefined,
                      [],
                    )}
                    data-v-9b049af3=""
                  ></input>
                  <button
                    className={__classes("secondary-button")}
                    data-v-9b049af3=""
                  >
                    {t("feeds.search")}
                  </button>
                  <label className={__classes("check")} data-v-9b049af3="">
                    <input
                      type="checkbox"
                      {...__input(
                        unread.value,
                        (__nextValue: any) => {
                          unread.value = __nextValue;
                        },
                        "checkbox",
                        undefined,
                        [],
                      )}
                      data-v-9b049af3=""
                    ></input>
                    {t("feeds.unread")}
                  </label>
                </form>
                {!items.value.length ? (
                  <p className={__classes("muted")} data-v-9b049af3="">
                    {t("feeds.noEntries")}
                  </p>
                ) : null}
                <Fragment>
                  {__each(items.value, (item) => (
                    <article
                      key={item.id as any}
                      className={__classes("feed-entry")}
                      data-v-9b049af3=""
                    >
                      <h3 data-v-9b049af3="">{item.title}</h3>
                      <p className={__classes("muted")} data-v-9b049af3="">
                        {t(
                          item.published ? "feeds.published" : "feeds.observed",
                        )}
                        {": "}
                        {new Date(
                          item.published || item.firstSeen,
                        ).toLocaleString()}
                      </p>
                      <p data-v-9b049af3="">{item.summary}</p>
                      <div
                        className={__classes("feed-actions")}
                        data-v-9b049af3=""
                      >
                        {item.url ? (
                          <a
                            href={item.url as any}
                            target="_blank"
                            rel="noopener noreferrer"
                            referrerPolicy="no-referrer"
                            data-v-9b049af3=""
                          >
                            {t("feeds.original")}
                          </a>
                        ) : null}
                        <button
                          disabled={busy.value as any}
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            read(item);
                          }}
                          data-v-9b049af3=""
                        >
                          {t(item.read ? "feeds.markUnread" : "feeds.markRead")}
                        </button>
                      </div>
                    </article>
                  ))}
                </Fragment>
                <div className={__classes("feed-actions")} data-v-9b049af3="">
                  <button
                    disabled={offset.value === (0 as any)}
                    className={__classes("secondary-button")}
                    onClick={(event: any) => {
                      offset.value = Math.max(0, offset.value - 50);
                      epoch++;
                      page();
                    }}
                    data-v-9b049af3=""
                  >
                    {t("feeds.previous")}
                  </button>
                  <button
                    disabled={(items.value.length < 50) as any}
                    className={__classes("secondary-button")}
                    onClick={(event: any) => {
                      offset.value += 50;
                      epoch++;
                      page();
                    }}
                    data-v-9b049af3=""
                  >
                    {t("feeds.next")}
                  </button>
                </div>
                <h2 className={__classes("delivery-title")} data-v-9b049af3="">
                  {t("feeds.deliveries")}
                </h2>
                <Fragment>
                  {__each(deliveries.value, (delivery) => (
                    <article
                      key={delivery.id as any}
                      className={__classes("feed-entry")}
                      data-v-9b049af3=""
                    >
                      <p data-v-9b049af3="">
                        {statusLabel(delivery.status)}
                        {" · "}
                        {delivery.attempts}
                        {" · "}
                        {new Date(delivery.created).toLocaleString()}
                      </p>
                      {delivery.error ? (
                        <p
                          className={__classes("feed-error")}
                          data-v-9b049af3=""
                        >
                          {display(delivery.error)}
                        </p>
                      ) : null}
                      {delivery.status === "failed" ? (
                        <button
                          disabled={busy.value as any}
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            perform(() =>
                              operationRequest("feeds.retry-delivery", {
                                params: {
                                  id: selected.value,
                                  deliveryId: delivery.id,
                                },
                              }),
                            );
                          }}
                          data-v-9b049af3=""
                        >
                          {t("feeds.retry")}
                        </button>
                      ) : null}
                    </article>
                  ))}
                </Fragment>
              </div>
            </div>
          </section>
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
