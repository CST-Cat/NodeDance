import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { ApiError, api } from "../api";
import { display, t } from "../i18n";
import { operationURL } from "../operations";
import {
  afterMount,
  afterRender,
  beforeDispose,
  cell,
  derived,
} from "../state";

import { __call, __classes, useController } from "../state";
import "./TerminalConsole.css";
type Props = {
  nodeId: string;
  targetKind: "host" | "container";
  containerId?: string;
  targetLabel: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function TerminalConsole(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      interface TerminalFrame {
        streamId: string;
        action: string;
        data?: string;
        message?: string;
        rows?: number;
        columns?: number;
        exitCode?: number;
      }
      const terminalElement = cell<HTMLDivElement>();
      const dialogElement = cell<HTMLElement>();
      let previousFocus: HTMLElement | null = null;
      let previousOverflow = "";
      function handleDialogKey(event: KeyboardEvent) {
        // Terminal keystrokes (including Escape and Tab) belong to the remote shell.
        if ((event.target as HTMLElement).closest(".terminal-screen")) return;
        if (event.key === "Escape") {
          event.preventDefault();
          close();
          return;
        }
        if (event.key !== "Tab") return;
        const controls = dialogElement.value?.querySelectorAll<HTMLElement>(
          'button:not(:disabled), textarea, [tabindex="0"]',
        );
        if (!controls?.length) return;
        const first = controls[0]!,
          last = controls[controls.length - 1]!;
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
      const statusKey = cell("m.389cde8ab951");
      const statusParams = cell<Record<string, unknown>>({});
      const status = derived(() => t(statusKey.value, statusParams.value));
      const error = cell("");
      let terminal: Terminal | undefined;
      let fit: FitAddon | undefined;
      let socket: WebSocket | undefined;
      let resizeObserver: ResizeObserver | undefined;
      let disposed = false;
      const ready = cell(false);
      let streamId = "";
      const finished = cell(false);
      let reconnectDelay = 500;
      let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
      function retryConnection() {
        if (disposed || finished.value || reconnectTimer) return;
        ready.value = false;
        statusKey.value = "terminal.reconnecting";
        reconnectTimer = setTimeout(() => {
          reconnectTimer = undefined;
          void connect(true);
        }, reconnectDelay);
        reconnectDelay = Math.min(5000, reconnectDelay * 2);
      }
      const encoder = new TextEncoder();
      const decoder = new TextDecoder("utf-8");
      function encodeBase64(data: Uint8Array): string {
        let binary = "";
        for (const byte of data) binary += String.fromCharCode(byte);
        return btoa(binary);
      }
      function sendInput(data: string) {
        if (!ready.value || !socket || socket.readyState !== WebSocket.OPEN)
          return;
        socket.send(
          JSON.stringify({
            streamId,
            action: "input",
            data: encodeBase64(encoder.encode(data)),
          }),
        );
      }
      function fitTerminal() {
        if (!fit || !terminal || !ready.value) return;
        fit.fit();
        socket?.send(
          JSON.stringify({
            streamId,
            action: "resize",
            rows: terminal.rows,
            columns: terminal.cols,
          }),
        );
      }
      function sendAuxiliary(value: string) {
        sendInput(value);
        terminal?.focus();
      }
      function close() {
        dispose();
        emit("close");
      }
      function endSession(nodeId: string, id: string) {
        if (!id) return;
        // End the exact session even when its socket is reconnecting.
        void api.cancelTerminal(nodeId, id).catch(() => undefined);
      }
      function dispose() {
        if (disposed) return;
        disposed = true;
        ready.value = false;
        clearTimeout(reconnectTimer);
        resizeObserver?.disconnect();
        if (socket?.readyState === WebSocket.OPEN) {
          socket.send(
            JSON.stringify({
              streamId,
              action: "close",
            }),
          );
        }
        socket?.close();
        terminal?.dispose();
        endSession(props.nodeId, streamId);
      }
      async function connect(resume = false) {
        try {
          const target =
            props.targetKind === "host"
              ? {
                  targetKind: "host" as const,
                }
              : {
                  targetKind: "container" as const,
                  containerId: props.containerId ?? "",
                };
          const authorization = resume
            ? await api.resumeTerminal(props.nodeId, streamId)
            : await api.createTerminal(props.nodeId, target);
          streamId = authorization.streamId;
          if (disposed) {
            // Unmount can race the POST response. Once the stream ID is known, revoke
            // the exact unused ticket so it does not hold a per-node slot until expiry.
            endSession(props.nodeId, streamId);
            return;
          }
          const url = await operationURL("terminal.connect", {
            query: {
              ticket: authorization.ticket,
            },
          });
          if (disposed) {
            endSession(props.nodeId, streamId);
            return;
          }
          const connection = new WebSocket(url);
          socket = connection;
          connection.onopen = () => {
            if (resume) {
              decoder.decode();
              terminal?.reset();
            }
            statusKey.value = "m.790beabf1d6b";
          };
          connection.onmessage = (event) => {
            if (disposed || socket !== connection) return;
            let message: {
              type?: string;
              frame?: TerminalFrame;
            };
            try {
              message = JSON.parse(String(event.data)) as typeof message;
            } catch {
              return;
            }
            const frame = message.frame;
            if (
              message.type !== "terminal" ||
              !frame ||
              frame.streamId !== streamId
            )
              return;
            if (frame.action === "ready") {
              ready.value = true;
              reconnectDelay = 500;
              error.value = "";
              statusKey.value = "m.5be0323e8adc";
              fitTerminal();
              terminal?.focus();
            } else if (frame.action === "output" && frame.data) {
              const bytes = Uint8Array.from(atob(frame.data), (character) =>
                character.charCodeAt(0),
              );
              terminal?.write(
                decoder.decode(bytes, {
                  stream: true,
                }),
              );
            } else if (frame.action === "error") {
              finished.value = true;
              ready.value = false;
              error.value = frame.message || t("m.df75c0b36f8c");
              statusKey.value = "m.89c4766afc85";
            } else if (frame.action === "exit") {
              finished.value = true;
              ready.value = false;
              statusKey.value = "m.d75d84cb855d";
              statusParams.value = {
                "0": frame.exitCode ?? "—",
              };
            } else if (frame.action === "closed") {
              finished.value = true;
              ready.value = false;
              statusKey.value = "m.fdfff0a9c49a";
            }
          };
          connection.onerror = () => {
            if (socket === connection && !disposed && !finished.value)
              statusKey.value = "terminal.reconnecting";
          };
          connection.onclose = () => {
            if (socket !== connection) return;
            ready.value = false;
            if (!disposed && !finished.value) retryConnection();
          };
        } catch (reason) {
          if (
            resume &&
            !disposed &&
            !(
              reason instanceof ApiError &&
              [401, 403, 410].includes(reason.status)
            )
          ) {
            retryConnection();
            return;
          }
          if (!disposed) {
            finished.value = true;
            ready.value = false;
            statusKey.value = "m.89c4766afc85";
            error.value =
              reason instanceof Error ? reason.message : t("m.9e8a26146154");
          }
        }
      }
      afterMount(async () => {
        previousFocus =
          document.activeElement instanceof HTMLElement
            ? document.activeElement
            : null;
        previousOverflow = document.body.style.overflow;
        document.body.style.overflow = "hidden";
        dialogElement.value?.querySelector<HTMLElement>("button")?.focus();
        const palette = getComputedStyle(
          terminalElement.value ?? document.documentElement,
        );
        terminal = new Terminal({
          cursorBlink: true,
          convertEol: false,
          scrollback: 3000,
          fontSize: 13,
          fontFamily:
            "JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
          theme: {
            background: palette.getPropertyValue("--product-canvas").trim(),
            foreground: palette.getPropertyValue("--product-text").trim(),
            cursor: palette.getPropertyValue("--accent-fill").trim(),
            selectionBackground: palette
              .getPropertyValue("--product-line-strong")
              .trim(),
          },
          allowProposedApi: false,
        });
        fit = new FitAddon();
        terminal.loadAddon(fit);
        if (terminalElement.value) terminal.open(terminalElement.value);
        terminal.onData(sendInput);
        resizeObserver = new ResizeObserver(() => {
          if (ready.value) fitTerminal();
          else fit?.fit();
        });
        if (terminalElement.value)
          resizeObserver.observe(terminalElement.value);
        await afterRender();
        fit.fit();
        await connect();
      });
      beforeDispose(() => {
        document.body.style.overflow = previousOverflow;
        if (previousFocus?.isConnected) previousFocus.focus();
        dispose();
      });
      return () => (
        <>
          <div
            data-testid="terminal-backdrop"
            className={__classes("terminal-backdrop")}
            onClick={(event: any) => {
              if (event.target !== event.currentTarget) return;
              __call(close, event);
            }}
            data-v-b8b9dc12=""
          >
            <section
              ref={__dom(dialogElement)}
              role="dialog"
              aria-modal="true"
              aria-labelledby="terminal-title"
              className={__classes("terminal-dialog")}
              onKeyDown={(event: any) => {
                __call(handleDialogKey, event);
              }}
              data-v-b8b9dc12=""
            >
              <header
                className={__classes("terminal-heading")}
                data-v-b8b9dc12=""
              >
                <div
                  className={__classes("terminal-heading-copy")}
                  data-v-b8b9dc12=""
                >
                  <span
                    className={__classes("terminal-eyebrow")}
                    data-v-b8b9dc12=""
                  >
                    {t("label.17001d87d5f0")}
                  </span>
                  <h2 id="terminal-title" data-v-b8b9dc12="">
                    {__props.targetLabel}
                  </h2>
                  <p data-v-b8b9dc12="">{status.value}</p>
                </div>
                <button
                  type="button"
                  aria-label={t("m.16707b59cf5e") as any}
                  className={__classes("terminal-close")}
                  onClick={(event: any) => {
                    __call(close, event);
                  }}
                  data-v-b8b9dc12=""
                >
                  {"×"}
                </button>
              </header>
              {error.value ? (
                <p
                  role="alert"
                  className={__classes("terminal-error")}
                  data-v-b8b9dc12=""
                >
                  {display(error.value)}
                </p>
              ) : null}
              <div
                ref={__dom(terminalElement)}
                data-testid="terminal-screen"
                aria-label={t("m.ce59564793be") as any}
                className={__classes("terminal-screen")}
                data-v-b8b9dc12=""
              ></div>
              <nav
                aria-label={t("m.151b2f2ae35f") as any}
                className={__classes("terminal-keys")}
                data-v-b8b9dc12=""
              >
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\t");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"Tab"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x03");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"Ctrl+C"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x04");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"Ctrl+D"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x1b");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"Esc"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x1b[A");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"↑"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x1b[B");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"↓"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x1b[D");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"←"}
                </button>
                <button
                  type="button"
                  disabled={!ready.value as any}
                  onClick={(event: any) => {
                    sendAuxiliary("\x1b[C");
                  }}
                  data-v-b8b9dc12=""
                >
                  {"→"}
                </button>
              </nav>
              <footer
                className={__classes("terminal-footer")}
                data-v-b8b9dc12=""
              >
                <span data-v-b8b9dc12="">
                  {t(
                    finished.value
                      ? "terminal.endedHint"
                      : ready.value
                        ? "m.b8bd8d5f7f2f"
                        : "terminal.reconnectingHint",
                  )}
                </span>
                <button
                  type="button"
                  onClick={(event: any) => {
                    __call(close, event);
                  }}
                  data-v-b8b9dc12=""
                >
                  {t("m.3eaf8d1b8148")}
                </button>
              </footer>
            </section>
          </div>
        </>
      );
    },
    input,
    ["nodeId", "targetKind", "containerId", "targetLabel"],
    [],
    {},
    true,
  );
}
