import { ManagementPage } from "./ManagementUI";
import "./PersonalSettings.css";
import { operationRequest } from "../api";
import { display, t } from "../i18n";
import {
  afterMount,
  beforeDispose,
  cell,
  watchState,
  useController,
} from "../state";
import {
  Button,
  Input,
  Textarea,
  Checkbox,
  Select,
  Alert,
} from "./ManagementUI";
import { Accordion } from "./arc/accordion/accordion";
import { SettingsGroup, SettingsRows } from "./SettingsGroup";
export default function FeedsCenter() {
  return useController(
    () => {
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
      let itemRequest: AbortController | undefined;
      let deliveryRequest: {id: string; controller: AbortController} | undefined;
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
      async function loadDeliveries(id: string) {
        if (deliveryRequest?.id === id) return;
        deliveryRequest?.controller.abort();
        const controller = new AbortController();
        deliveryRequest = {id, controller};
        try {
          const result = await operationRequest<{deliveries: Delivery[]}>("feeds.deliveries", {params: {id}}, {signal: controller.signal});
          if (!stopped && !controller.signal.aborted && selected.value === id) deliveries.value = result.deliveries;
        } catch (e) {
          if (!stopped && !controller.signal.aborted && selected.value === id) error.value = message(e);
        } finally {
          if (deliveryRequest?.controller === controller) deliveryRequest = undefined;
        }
      }
      async function page(withDeliveries = true) {
        if (stopped || !selected.value) return;
        itemRequest?.abort();
        const controller = new AbortController();
        itemRequest = controller;
        const current = epoch,
          id = selected.value;
        if (withDeliveries) void loadDeliveries(id);
        try {
          const entries = await operationRequest<{
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
            }, {signal: controller.signal});
          if (stopped || controller.signal.aborted || current !== epoch || id !== selected.value) return;
          items.value = entries.items;
        } catch (e) {
          if (current === epoch && !stopped && !controller.signal.aborted) error.value = message(e);
        } finally {
          if (itemRequest === controller) itemRequest = undefined;
        }
      }
      async function poll() {
        if (!busy.value) {
          const previous = selected.value;
          await reload();
          if (!busy.value && selected.value === previous) await page();
        }
        if (!stopped) timer = setTimeout(poll, 5000);
      }
      async function perform(call: () => Promise<unknown>) {
        if (stopped || busy.value) return;
        busy.value = true;
        epoch++;
        itemRequest?.abort();
        deliveryRequest?.controller.abort();
        deliveryRequest = undefined;
        error.value = "";
        try {
          await call();
          if (stopped) return;
          await reload();
          await page();
        } catch (e) {
          if (!stopped) error.value = message(e);
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
      watchState([selected, unread], (value, previous) => {
        epoch++;
        offset.value = 0;
        removeConfirmed.value = false;
        if (value[0] !== previous[0]) {
          items.value = [];
          deliveries.value = [];
        }
        if (!busy.value) void page(value[0] !== previous[0]);
      });
      afterMount(() => {
        void operationRequest<Options>("feeds.options").then(result => {
          if (stopped) return;
          options.value = result;
          if (!editing.value && draft.value.intervalMinutes === 30) draft.value.intervalMinutes = result.defaultIntervalMinutes;
        }).catch(e => {if (!stopped) error.value = message(e);});
        void operationRequest<{channels: Channel[]}>("channels.list").then(result => {
          if (!stopped) channels.value = result.channels;
        }).catch(e => {if (!stopped) error.value = message(e);});
        void poll();
      });
      beforeDispose(() => {
        stopped = true;
        epoch++;
        if (timer) clearTimeout(timer);
        itemRequest?.abort();
        deliveryRequest?.controller.abort();
      });
      return () => (
        <ManagementPage
          registryStyles
          className="preferences-page workspace-settings"
        >
          <header className="workspace-heading">
            <h1>{t("feeds.title")}</h1>
          </header>
          <div className="settings-sections">
            <section
              data-testid="feeds-center"
              className="workspace-section settings-section settings-integrated feed-settings"
              aria-labelledby="settings-feeds-heading"
            >
              <div className="workspace-section-heading">
                <h2 id="settings-feeds-heading">
                  {t(editing.value ? "feeds.edit" : "feeds.add")}
                </h2>
              </div>
              {error.value ? (
                <Alert role="alert" tone="danger" title={t("management.error")}>
                  {display(error.value)}
                </Alert>
              ) : null}
              <form
                className="feed-form settings-detail-list"
                onSubmit={(event) => {
                  event.preventDefault();
                  void save();
                }}
              >
                <SettingsGroup className="feed-settings-group">
                  <label className="parameter-row">
                    <span>{t("feeds.name")}</span>
                    <Input
                      name="feedName"
                      required
                      value={draft.value.name}
                      onChange={(event) => {
                        draft.value.name = event.target.value;
                      }}
                    />
                  </label>
                  <label className="parameter-row">
                    <span>{t("feeds.url")}</span>
                    <Input
                      name="feedUrl"
                      type="url"
                      required
                      value={draft.value.url}
                      onChange={(event) => {
                        draft.value.url = event.target.value;
                      }}
                    />
                  </label>
                  <label className="parameter-row">
                    <span>{t("feeds.interval")}</span>
                    <span className="feed-interval-controls">
                      <Select
                        aria-label={t("feeds.interval")}
                        name="feedInterval"
                        value={String(draft.value.intervalMinutes)}
                        onValueChange={(value) => {
                          draft.value.intervalMinutes = Number(value);
                        }}
                        options={Array.from(
                          new Set([
                            ...(options.value?.intervalMinutes ?? []),
                            draft.value.intervalMinutes,
                          ]),
                        ).map((minutes) => ({
                          value: String(minutes),
                          label: `${minutes} ${t("feeds.minutes")}`,
                        }))}
                      />
                      <Input
                        name="feedCustomInterval"
                        type="number"
                        aria-label={t("feeds.customInterval")}
                        min={options.value?.minimumIntervalMinutes}
                        max={options.value?.maximumIntervalMinutes}
                        value={draft.value.intervalMinutes}
                        onChange={(event) => {
                          draft.value.intervalMinutes = Number(
                            event.target.value,
                          );
                        }}
                      />
                    </span>
                  </label>
                  <label className="parameter-row">
                    <span>{t("feeds.enabled")}</span>
                    <span className="parameter-control">
                      <Checkbox
                        aria-label={t("feeds.enabled")}
                        checked={draft.value.enabled}
                        onCheckedChange={(value) => {
                          draft.value.enabled = value === true;
                        }}
                      />
                    </span>
                  </label>
                  <label className="parameter-row">
                    <span>{t("feeds.notifications")}</span>
                    <Select
                      aria-label={t("feeds.notifications")}
                      name="feedNotificationMode"
                      value={draft.value.notificationMode}
                      onValueChange={(value) => {
                        draft.value.notificationMode = value;
                      }}
                      options={[
                        { value: "off", label: t("feeds.off") },
                        { value: "immediate", label: t("feeds.immediate") },
                        { value: "digest", label: t("feeds.digest") },
                      ]}
                    />
                  </label>
                  <label className="parameter-row">
                    <span>{t("feeds.keywords")}</span>
                    <Textarea
                      name="feedKeywords"
                      rows={3}
                      value={keywordText.value}
                      onChange={(event) => {
                        keywordText.value = event.target.value;
                      }}
                    />
                  </label>
                  {draft.value.notificationMode === "digest" ? (
                    <label className="parameter-row">
                      <span>{t("feeds.digestInterval")}</span>
                      <Input
                        type="number"
                        min={options.value?.minimumIntervalMinutes}
                        max={options.value?.maximumIntervalMinutes}
                        value={draft.value.digestMinutes}
                        onChange={(event) => {
                          draft.value.digestMinutes = Number(
                            event.target.value,
                          );
                        }}
                      />
                    </label>
                  ) : null}
                  {draft.value.notificationMode !== "off" ? (
                    <div className="parameter-row">
                      <span>{t("feeds.channels")}</span>
                      <div className="settings-detail-list">
                        {channels.value
                          .filter((channel) => channel.enabled)
                          .map((channel) => (
                            <label className="settings-check" key={channel.id}>
                              <Checkbox
                                checked={draft.value.channelIds.includes(
                                  channel.id,
                                )}
                                onCheckedChange={(value) => {
                                  draft.value.channelIds =
                                    value === true
                                      ? [...draft.value.channelIds, channel.id]
                                      : draft.value.channelIds.filter(
                                          (id) => id !== channel.id,
                                        );
                                }}
                              />
                              {channel.name}
                            </label>
                          ))}
                        {!channels.value.some((channel) => channel.enabled) ? (
                          <span>{t("feeds.noChannels")}</span>
                        ) : null}
                      </div>
                    </div>
                  ) : null}
                </SettingsGroup>
                <div className="settings-section-actions">
                  <Button type="submit" disabled={busy.value || !options.value}>
                    {t("feeds.save")}
                  </Button>
                  {editing.value ? (
                    <Button type="button" variant="secondary" onClick={reset}>
                      {t("feeds.cancelEdit")}
                    </Button>
                  ) : null}
                </div>
              </form>
              {feeds.value.length ? (
                <SettingsGroup>
                  {feeds.value.map((feed) => (
                    <article
                      key={feed.id}
                      className="feed-subscription settings-detail-list"
                    >
                      <div className="settings-record-row">
                        <div className="settings-record-info">
                          <strong>{feed.name}</strong>
                          <span>
                            {feed.intervalMinutes} {t("feeds.minutes")} ·{" "}
                            {statusLabel(feed.status)}
                          </span>
                        </div>
                        <Button
                          className="feed-select"
                          variant="ghost"
                          aria-label={feed.name}
                          aria-pressed={selected.value === feed.id}
                          onClick={() => {
                            selected.value = feed.id;
                          }}
                        >
                          {t("feeds.entries")}
                        </Button>
                      </div>
                      {feed.error ? (
                        <Alert tone="danger" title={t("management.error")}>
                          {display(feed.error)}
                        </Alert>
                      ) : null}
                      <div className="settings-detail-list">
                        <span>
                          {t("feeds.lastSuccess")}:{" "}
                          {feed.lastSuccess.startsWith("0001")
                            ? "—"
                            : new Date(feed.lastSuccess).toLocaleString()}
                        </span>
                        <span>
                          {t("feeds.nextCheck")}:{" "}
                          {new Date(feed.nextCheck).toLocaleString()}
                        </span>
                      </div>
                      <div className="feed-actions settings-section-actions">
                        <Button
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => edit(feed)}
                        >
                          {t("feeds.edit")}
                        </Button>
                        <Button
                          variant="secondary"
                          disabled={busy.value || !feed.enabled}
                          onClick={() => void action(feed, "refresh")}
                        >
                          {t("feeds.refresh")}
                        </Button>
                        {["queued", "running"].includes(feed.status) ? (
                          <Button
                            variant="secondary"
                            disabled={busy.value}
                            onClick={() => void action(feed, "cancel")}
                          >
                            {t("feeds.cancelRefresh")}
                          </Button>
                        ) : null}
                        <Button
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => void toggle(feed)}
                        >
                          {t(feed.enabled ? "feeds.disable" : "feeds.enable")}
                        </Button>
                      </div>
                      {selected.value === feed.id ? (
                        <>
                          <label className="settings-check">
                            <Checkbox
                              checked={removeConfirmed.value}
                              onCheckedChange={(value) => {
                                removeConfirmed.value = value === true;
                              }}
                            />
                            {t("feeds.deleteConfirm")}
                          </label>
                          <div className="settings-section-actions">
                            <Button
                              variant="secondary"
                              disabled={busy.value || !removeConfirmed.value}
                              onClick={() => void remove(feed)}
                            >
                              {t("feeds.delete")}
                            </Button>
                          </div>
                        </>
                      ) : null}
                    </article>
                  ))}
                </SettingsGroup>
              ) : (
                <p>{t("feeds.empty")}</p>
              )}
              {selected.value ? (
                <SettingsGroup>
                  <form
                    className="feed-actions settings-feed-search"
                    onSubmit={(event) => {
                      event.preventDefault();
                      offset.value = 0;
                      epoch++;
                      void page(false);
                    }}
                  >
                    <Input
                      type="search"
                      aria-label={t("feeds.search")}
                      value={search.value}
                      onChange={(event) => {
                        search.value = event.target.value;
                      }}
                    />
                    <Button type="submit" variant="secondary">
                      {t("feeds.search")}
                    </Button>
                    <label className="settings-check">
                      <Checkbox
                        checked={unread.value}
                        onCheckedChange={(value) => {
                          unread.value = value === true;
                        }}
                      />
                      {t("feeds.unread")}
                    </label>
                  </form>
                  {!items.value.length ? <p>{t("feeds.noEntries")}</p> : null}
                  {items.value.map((item) => (
                    <article
                      className="feed-entry settings-detail-list"
                      key={item.id}
                    >
                      <h3>{item.title}</h3>
                      <span>
                        {t(
                          item.published ? "feeds.published" : "feeds.observed",
                        )}
                        :{" "}
                        {new Date(
                          item.published || item.firstSeen,
                        ).toLocaleString()}
                      </span>
                      <p>{item.summary}</p>
                      <div className="settings-section-actions">
                        {item.url ? (
                          <a
                            href={item.url}
                            target="_blank"
                            rel="noopener noreferrer"
                            referrerPolicy="no-referrer"
                          >
                            {t("feeds.original")}
                          </a>
                        ) : null}
                        <Button
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => void read(item)}
                        >
                          {t(item.read ? "feeds.markUnread" : "feeds.markRead")}
                        </Button>
                      </div>
                    </article>
                  ))}
                  <div className="settings-section-actions">
                    <Button
                      variant="secondary"
                      disabled={offset.value === 0}
                      onClick={() => {
                        offset.value = Math.max(0, offset.value - 50);
                        epoch++;
                        void page(false);
                      }}
                    >
                      {t("feeds.previous")}
                    </Button>
                    <Button
                      variant="secondary"
                      disabled={items.value.length < 50}
                      onClick={() => {
                        offset.value += 50;
                        epoch++;
                        void page(false);
                      }}
                    >
                      {t("feeds.next")}
                    </Button>
                  </div>
                  {deliveries.value.length ? (
                    <Accordion
                      defaultOpen={-1}
                      items={[
                        {
                          title: t("feeds.deliveries"),
                          content: (
                            <SettingsRows>
                              {deliveries.value.map((delivery) => (
                                <article
                                  key={delivery.id}
                                  className="settings-detail-list"
                                >
                                  <span>
                                    {statusLabel(delivery.status)} ·{" "}
                                    {delivery.attempts} ·{" "}
                                    {new Date(
                                      delivery.created,
                                    ).toLocaleString()}
                                  </span>
                                  {delivery.error ? (
                                    <p>{display(delivery.error)}</p>
                                  ) : null}
                                  {delivery.status === "failed" ? (
                                    <Button
                                      variant="secondary"
                                      disabled={busy.value}
                                      onClick={() =>
                                        void perform(() =>
                                          operationRequest(
                                            "feeds.retry-delivery",
                                            {
                                              params: {
                                                id: selected.value,
                                                deliveryId: delivery.id,
                                              },
                                            },
                                          ),
                                        )
                                      }
                                    >
                                      {t("feeds.retry")}
                                    </Button>
                                  ) : null}
                                </article>
                              ))}
                            </SettingsRows>
                          ),
                        },
                      ]}
                    />
                  ) : null}
                </SettingsGroup>
              ) : null}
            </section>
          </div>
        </ManagementPage>
      );
    },
    {},
    [],
    [],
    {},
    true,
  );
}
