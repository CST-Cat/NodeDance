import { Button } from "./ManagementUI";
import { display, locale, t } from "../i18n";
import {
  beforeDispose,
  cell,
  derived,
  watchState,
} from "../state";
import {
  installScrollEdges,
  type FollowState,
  type ScrollEdges,
} from "../scroll-edge";
import {
  createContainerStreamSocket,
  decodeContainerLogData,
  type ContainerLogFrame,
  type ContainerStatsFrame,
  type ContainerStreamEnvelope,
  type ContainerStreamKind,
  type ContainerStreamReady,
} from "../streamApi";

import { __classes, useController } from "../state";
import UiIcon from "./UiIcon";
import "./ContainerStreams.css";
type Props = {
  nodeId: string;
  containerId: string;
  disabled?: boolean;
  statsAvailable?: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ContainerStreams(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      type StreamState = "idle" | "connecting" | "connected" | "error";
      const logState = cell<StreamState>("idle");
      const statsState = cell<StreamState>("idle");
      const logError = cell("");
      const statsError = cell("");
      const logText = cell("");
      const logBytes = cell(0);
      const logChannels = cell(new Set<"stdout" | "stderr">());
      const statsSnapshot = cell<ContainerStatsFrame["snapshot"]>();
      const statsSamples = cell(0);
      const connectionRevision = {
        logs: 0,
        stats: 0,
      };
      const sockets: Partial<Record<ContainerStreamKind, WebSocket>> = {};
      const expectedClose = new Set<ContainerStreamKind>();
      const sequence: Partial<Record<ContainerStreamKind, number>> = {};
      const logDecoders = {
        stdout: new TextDecoder(),
        stderr: new TextDecoder(),
      };
      let pendingLogChunks: string[] = [];
      let pendingLogLength = 0;
      let pendingLogBytes = 0;
      let logFlushTimer: ReturnType<typeof setInterval> | undefined;
      // OpenUI useClaudeScrollPinning port: streaming logs auto-follow the
      // bottom edge and yield as soon as the user scrolls away; the fade
      // sensor marks edges while text is appended between scroll events.
      const logOutput = cell<HTMLPreElement>();
      const logFollow = cell<FollowState>({ pinned: true, canJump: false });
      let logEdges: ScrollEdges | undefined;
      function syncLogEdges() {
        const element = logOutput.value;
        if (logEdges) {
          logEdges.dispose();
          logEdges = undefined;
        }
        if (element)
          logEdges = installScrollEdges(element, {
            follow: {
              onChange: (state) => {
                logFollow.value = state;
              },
            },
          });
      }
      watchState(logOutput, syncLogEdges, { immediate: true });
      const statsTime = derived(() =>
        statsSnapshot.value?.observed_at
          ? new Date(statsSnapshot.value.observed_at).toLocaleTimeString(
              locale.value,
            )
          : t("m.a3cf429bd3c2"),
      );
      const LOG_TEXT_LIMIT = 256 * 1024;
      const PENDING_LOG_TEXT_LIMIT = 64 * 1024;
      function stateFor(kind: ContainerStreamKind) {
        return kind === "logs" ? logState : statsState;
      }
      function errorFor(kind: ContainerStreamKind) {
        return kind === "logs" ? logError : statsError;
      }
      function stateLabel(state: StreamState): string {
        if (state === "connecting") return t("m.694186911ae3");
        if (state === "connected") return t("m.f8d0ccc162bf");
        if (state === "error") return t("m.30f12a33e524");
        return t("m.a0a4c71b52cc");
      }
      function explainStreamError(code: string): string {
        const messages: Record<string, string> = {
          unavailable: t("m.3c2431b9bd9a"),
          not_running: t("m.d2693f566461"),
          invalid_request: t("m.999d94cb7d0b"),
          stream_limit: t("m.70df2879765e"),
          slow_consumer: t("m.ec57a4831394"),
          engine_error: t("m.4c13a09d69af"),
        };
        return messages[code] ?? t("m.2ea0b2a956a8");
      }
      function fail(kind: ContainerStreamKind, message: string) {
        errorFor(kind).value = message;
        stateFor(kind).value = "error";
        expectedClose.add(kind);
        sockets[kind]?.close(1000, "stream ended");
      }
      function appendLog(frame: ContainerLogFrame) {
        if (
          frame.containerId !== props.containerId ||
          (frame.channel !== "stdout" && frame.channel !== "stderr")
        ) {
          fail("logs", t("m.1a594d27c332"));
          return;
        }
        if (!logChannels.value.has(frame.channel))
          logChannels.value = new Set([...logChannels.value, frame.channel]);
        let bytes: number;
        try {
          bytes = atob(frame.data).length;
          const decoded = decodeContainerLogData(
            frame.data,
            logDecoders[frame.channel],
          );
          pendingLogBytes += bytes;
          if (decoded) appendPendingLogText(`[${frame.channel}] ${decoded}`);
        } catch {
          fail("logs", t("m.593a3d7e3f1d"));
        }
      }
      function appendPendingLogText(text: string) {
        pendingLogChunks.push(text);
        pendingLogLength += text.length;
        while (
          pendingLogLength > PENDING_LOG_TEXT_LIMIT &&
          pendingLogChunks.length > 0
        ) {
          const excess = pendingLogLength - PENDING_LOG_TEXT_LIMIT;
          const first = pendingLogChunks[0];
          if (first.length <= excess) {
            pendingLogChunks.shift();
            pendingLogLength -= first.length;
          } else {
            pendingLogChunks[0] = first.slice(excess);
            pendingLogLength -= excess;
          }
        }
      }
      function flushLogBuffer() {
        if (pendingLogBytes > 0) {
          logBytes.value += pendingLogBytes;
          pendingLogBytes = 0;
        }
        if (pendingLogChunks.length === 0) return;
        logText.value += pendingLogChunks.join("");
        pendingLogChunks = [];
        pendingLogLength = 0;
        if (logText.value.length > LOG_TEXT_LIMIT) {
          const trimAt = logText.value.indexOf(
            "\n",
            logText.value.length - LOG_TEXT_LIMIT,
          );
          logText.value = logText.value.slice(
            trimAt >= 0 ? trimAt + 1 : -LOG_TEXT_LIMIT,
          );
        }
      }
      function startLogFlush() {
        if (logFlushTimer !== undefined) return;
        logFlushTimer = setInterval(flushLogBuffer, 50);
      }
      function stopLogFlush() {
        if (logFlushTimer !== undefined) {
          clearInterval(logFlushTimer);
          logFlushTimer = undefined;
        }
        flushLogBuffer();
      }
      function acceptStats(frame: ContainerStatsFrame) {
        if (
          !frame.snapshot ||
          frame.snapshot.container_id !== props.containerId
        ) {
          fail("stats", t("m.091a17469205"));
          return;
        }
        statsSnapshot.value = frame.snapshot;
        statsSamples.value++;
      }
      function flushLogDecoders() {
        for (const channel of ["stdout", "stderr"] as const) {
          const text = logDecoders[channel].decode();
          if (text) appendPendingLogText(`[${channel}] ${text}`);
        }
        flushLogBuffer();
      }
      async function open(kind: ContainerStreamKind) {
        if (
          props.disabled ||
          (kind === "stats" && !props.statsAvailable) ||
          stateFor(kind).value === "connecting" ||
          stateFor(kind).value === "connected"
        )
          return;
        errorFor(kind).value = "";
        if (kind === "logs") {
          logText.value = "";
          logBytes.value = 0;
          logChannels.value = new Set();
          pendingLogChunks = [];
          pendingLogLength = 0;
          pendingLogBytes = 0;
          logDecoders.stdout = new TextDecoder();
          logDecoders.stderr = new TextDecoder();
        } else {
          statsSnapshot.value = undefined;
          statsSamples.value = 0;
        }
        sequence[kind] = 0;
        expectedClose.delete(kind);
        stateFor(kind).value = "connecting";
        const revision = ++connectionRevision[kind];
        let socket: WebSocket;
        try {
          socket = await createContainerStreamSocket(
            kind,
            props.nodeId,
            props.containerId,
          );
          if (revision !== connectionRevision[kind]) {
            socket.close();
            return;
          }
        } catch {
          if (revision !== connectionRevision[kind]) return;
          stateFor(kind).value = "error";
          errorFor(kind).value = t("m.f5e901036eb9");
          return;
        }
        sockets[kind] = socket;
        if (kind === "logs") startLogFlush();
        socket.onmessage = (event) => {
          let envelope: ContainerStreamEnvelope;
          try {
            envelope = JSON.parse(
              String(event.data),
            ) as ContainerStreamEnvelope;
          } catch {
            fail(kind, t("m.47ce61735a6a"));
            return;
          }
          if (
            !envelope ||
            typeof envelope.type !== "string" ||
            !Number.isSafeInteger(envelope.sequence) ||
            envelope.sequence <= (sequence[kind] ?? 0)
          ) {
            fail(kind, t("m.469dce4e4576"));
            return;
          }
          sequence[kind] = envelope.sequence;
          if (envelope.type === "container_stream_ready") {
            const ready = envelope.payload as ContainerStreamReady;
            if (
              ready.kind !== kind ||
              ready.containerId !== props.containerId
            ) {
              fail(kind, t("m.95b4da8af84b"));
              return;
            }
            stateFor(kind).value = "connected";
            return;
          }
          if (envelope.type === "container_stream_error") {
            fail(
              kind,
              explainStreamError(
                (
                  envelope.payload as {
                    code?: string;
                  }
                )?.code ?? "engine_error",
              ),
            );
            return;
          }
          if (envelope.type === "container_stream_end") {
            expectedClose.add(kind);
            socket.close(1000, "stream ended");
            return;
          }
          if (envelope.type === "container_stream_heartbeat") return;
          if (envelope.type === "container_log" && kind === "logs") {
            appendLog(envelope.payload as ContainerLogFrame);
            return;
          }
          if (envelope.type === "container_stats" && kind === "stats") {
            acceptStats(envelope.payload as ContainerStatsFrame);
            return;
          }
          fail(kind, t("m.d5fa3fb6358f"));
        };
        socket.onerror = () => {
          if (
            stateFor(kind).value === "connecting" ||
            stateFor(kind).value === "connected"
          )
            fail(kind, t("m.a6a85e2b610a"));
        };
        socket.onclose = () => {
          if (sockets[kind] !== socket) return;
          delete sockets[kind];
          if (kind === "logs") {
            flushLogDecoders();
            stopLogFlush();
          }
          if (expectedClose.has(kind)) {
            expectedClose.delete(kind);
            if (stateFor(kind).value !== "error") stateFor(kind).value = "idle";
            return;
          }
          if (
            stateFor(kind).value === "connecting" ||
            stateFor(kind).value === "connected"
          ) {
            stateFor(kind).value = "error";
            errorFor(kind).value = t("m.4c50e0ef9a32");
          }
        };
      }
      function close(kind: ContainerStreamKind) {
        connectionRevision[kind]++;
        const socket = sockets[kind];
        if (!socket) {
          if (kind === "logs") stopLogFlush();
          stateFor(kind).value = "idle";
          errorFor(kind).value = "";
          return;
        }
        expectedClose.add(kind);
        stateFor(kind).value = "idle";
        errorFor(kind).value = "";
        if (kind === "logs") {
          flushLogDecoders();
          stopLogFlush();
        }
        socket.close(1000, "user closed stream");
      }
      function metric(
        value:
          | {
              state: string;
              value?: number;
              reason?: string;
            }
          | undefined,
        suffix = "",
      ): string {
        if (
          !value ||
          value.state !== "ok" ||
          typeof value.value !== "number" ||
          !Number.isFinite(value.value)
        ) {
          return value?.reason
            ? t("m.a92ddd73cc1b", {
                "0": value.reason,
              })
            : t("m.4d8c1c5b4283");
        }
        return `${value.value.toFixed(1)}${suffix}`;
      }
      function memoryMetric(): string {
        const memory = statsSnapshot.value?.memory;
        if (!memory || memory.state !== "ok" || !memory.value)
          return memory?.reason
            ? t("m.a92ddd73cc1b", {
                "0": memory.reason,
              })
            : t("m.4d8c1c5b4283");
        return `${memory.value.used_percent.toFixed(1)}% · ${formatBytes(memory.value.used_bytes)} / ${formatBytes(memory.value.limit_bytes)}`;
      }
      function formatBytes(value: number): string {
        if (!Number.isFinite(value) || value < 0) return t("m.4d8c1c5b4283");
        const units = ["B", "KiB", "MiB", "GiB", "TiB"];
        let scaled = value;
        let index = 0;
        while (scaled >= 1024 && index < units.length - 1) {
          scaled /= 1024;
          index++;
        }
        return `${scaled.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
      }
      function rateMetric(
        metricValue:
          | {
              state: string;
              value?: {
                received_bytes_per_second: number;
                sent_bytes_per_second: number;
              };
              reason?: string;
            }
          | undefined,
      ): string {
        if (!metricValue || metricValue.state !== "ok" || !metricValue.value)
          return metricValue?.reason
            ? t("m.a92ddd73cc1b", {
                "0": metricValue.reason,
              })
            : t("m.4d8c1c5b4283");
        return `↓ ${formatBytes(metricValue.value.received_bytes_per_second)}/s · ↑ ${formatBytes(metricValue.value.sent_bytes_per_second)}/s`;
      }
      function blockIOMetric(
        metricValue:
          | {
              state: string;
              value?: {
                read_bytes_per_second: number;
                write_bytes_per_second: number;
              };
              reason?: string;
            }
          | undefined,
      ): string {
        if (!metricValue || metricValue.state !== "ok" || !metricValue.value)
          return metricValue?.reason
            ? t("m.a92ddd73cc1b", {
                "0": metricValue.reason,
              })
            : t("m.4d8c1c5b4283");
        return t("m.17e2e66586d0", {
          "0": formatBytes(metricValue.value.read_bytes_per_second),
          "1": formatBytes(metricValue.value.write_bytes_per_second),
        });
      }
      beforeDispose(() => {
        logEdges?.dispose();
        for (const kind of ["logs", "stats"] as const) close(kind);
        stopLogFlush();
      });
      return () => (
        <>
          <section
            data-testid="container-streams"
            data-container-id={__props.containerId as any}
            aria-label={t("m.4b0e84bd108a") as any}
            className={__classes("container-streams")}
            data-v-e4bd43c8=""
          >
            <div
              className={__classes("container-stream-actions")}
              data-v-e4bd43c8=""
            >
              {logState.value !== "connecting" &&
              logState.value !== "connected" ? (
                <Button variant="secondary"
                  type="button"
                  disabled={__props.disabled as any}
                  onClick={(event: any) => {
                    open("logs");
                  }}
                  data-v-e4bd43c8=""
                >
                  {t("m.659875587949")}
                </Button>
              ) : (
                <Button variant="secondary"
                  type="button"
                  className={__classes("active")}
                  onClick={(event: any) => {
                    close("logs");
                  }}
                  data-v-e4bd43c8=""
                >
                  {t("m.8949c1484d4d")}
                </Button>
              )}
              <span
                data-state={logState.value as any}
                data-testid="stream-log-state"
                role="status"
                className={__classes("stream-label")}
                data-v-e4bd43c8=""
              >
                {t("m.a1c88ca8b9b4")}
                {stateLabel(logState.value)}
              </span>
              {statsState.value !== "connecting" &&
              statsState.value !== "connected" ? (
                <Button variant="secondary"
                  type="button"
                  disabled={
                    __props.disabled || (!__props.statsAvailable as any)
                  }
                  onClick={(event: any) => {
                    open("stats");
                  }}
                  data-v-e4bd43c8=""
                >
                  {t("m.5f8faa43a270")}
                </Button>
              ) : (
                <Button variant="secondary"
                  type="button"
                  className={__classes("active")}
                  onClick={(event: any) => {
                    close("stats");
                  }}
                  data-v-e4bd43c8=""
                >
                  {t("m.31d2201e9d0b")}
                </Button>
              )}
              <span
                data-state={statsState.value as any}
                data-testid="stream-stats-state"
                role="status"
                className={__classes("stream-label")}
                data-v-e4bd43c8=""
              >
                {t("m.ba07d9987c05")}
                {stateLabel(statsState.value)}
              </span>
            </div>
            {__props.disabled ? (
              <p
                data-testid="stream-disabled"
                className={__classes("stream-hint")}
                data-v-e4bd43c8=""
              >
                {t("m.4f75ac6c5ef0")}
              </p>
            ) : !__props.statsAvailable ? (
              <p className={__classes("stream-hint")} data-v-e4bd43c8="">
                {t("m.a6a6536f5d05")}
              </p>
            ) : null}
            {logError.value ? (
              <p
                role="alert"
                data-testid="stream-log-error"
                className={__classes("stream-error")}
                data-v-e4bd43c8=""
              >
                {display(logError.value)}
              </p>
            ) : null}
            {statsError.value ? (
              <p
                role="alert"
                data-testid="stream-stats-error"
                className={__classes("stream-error")}
                data-v-e4bd43c8=""
              >
                {display(statsError.value)}
              </p>
            ) : null}
            {logState.value === "connected" || logText.value ? (
              <div className={__classes("stream-log-panel")} data-v-e4bd43c8="">
                <div
                  className={__classes("stream-panel-heading")}
                  data-v-e4bd43c8=""
                >
                  <strong data-v-e4bd43c8="">{t("m.11824b8117a5")}</strong>
                  <span
                    data-testid="stream-log-bytes"
                    data-bytes={logBytes.value as any}
                    data-v-e4bd43c8=""
                  >
                    {formatBytes(logBytes.value)}
                  </span>
                </div>
                <pre
                  data-testid="stream-log-output"
                  data-bytes={logBytes.value as any}
                  data-channels={Array.from(logChannels.value).join(",") as any}
                  ref={__dom(logOutput)}
                  className={__classes("scroll-fade")}
                  data-v-e4bd43c8=""
                >
                  {logText.value || t("m.073e53c8c4c3")}
                </pre>
                {logFollow.value.canJump && !logFollow.value.pinned ? (
                  <Button variant="secondary"
                    type="button"
                    data-testid="stream-log-follow"
                    className={__classes("stream-log-jump")}
                    onClick={() => {
                      logEdges?.followLatest();
                    }}
                    data-v-e4bd43c8=""
                  >
                    <UiIcon name="arrowDown"></UiIcon>
                    <span>{t("stream.jumpToLatest")}</span>
                  </Button>
                ) : null}
              </div>
            ) : null}
            {statsState.value === "connected" || statsSnapshot.value ? (
              <div
                data-testid="stream-stats-snapshot"
                data-samples={statsSamples.value as any}
                className={__classes("stream-stats-panel")}
                data-v-e4bd43c8=""
              >
                <div
                  className={__classes("stream-panel-heading")}
                  data-v-e4bd43c8=""
                >
                  <strong data-v-e4bd43c8="">{t("m.9633f25598d6")}</strong>
                  <span data-v-e4bd43c8="">{statsTime.value}</span>
                </div>
                <dl data-v-e4bd43c8="">
                  <div data-v-e4bd43c8="">
                    <dt data-v-e4bd43c8="">{"CPU"}</dt>
                    <dd data-testid="stream-cpu" data-v-e4bd43c8="">
                      {metric(statsSnapshot.value?.cpu_percent, "%")}
                    </dd>
                  </div>
                  <div data-v-e4bd43c8="">
                    <dt data-v-e4bd43c8="">{t("m.7d8f8c37ec78")}</dt>
                    <dd data-testid="stream-memory" data-v-e4bd43c8="">
                      {memoryMetric()}
                    </dd>
                  </div>
                  <div data-v-e4bd43c8="">
                    <dt data-v-e4bd43c8="">{t("m.97b31b5d63f5")}</dt>
                    <dd data-testid="stream-network" data-v-e4bd43c8="">
                      {rateMetric(statsSnapshot.value?.network)}
                    </dd>
                  </div>
                  <div data-v-e4bd43c8="">
                    <dt data-v-e4bd43c8="">{t("m.6acb47100357")}</dt>
                    <dd data-testid="stream-block-io" data-v-e4bd43c8="">
                      {blockIOMetric(statsSnapshot.value?.block_io)}
                    </dd>
                  </div>
                </dl>
              </div>
            ) : null}
          </section>
        </>
      );
    },
    input,
    ["nodeId", "containerId", "disabled", "statsAvailable"],
    [],
    {},
    true,
  );
}
