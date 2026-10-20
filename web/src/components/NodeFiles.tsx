import { createPortal } from "react-dom";
import { fileNameMarkup } from "../path-display";
import type { FinderFile } from "../elfinder-runtime";
import UiPath from "./UiPath";
import UiLoadState from "./UiLoadState";
import { loadFinder, type Finder } from "../elfinder-runtime";
import { createFileManager, type FileObservation } from "../file-manager";
import { locale, t } from "../i18n";
import { afterMount, beforeDispose, cell, watchState } from "../state";

import { Fragment } from "react";
import { __classes, __each, useController } from "../state";
type Props = {
  nodeId: string;
  nodeName: string;
  path?: string;
  directoryPicker?: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export const preloadFileManager = loadFinder;

export default function NodeFiles(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const host = cell<HTMLElement>(),
        error = cell(""),
        loading = cell(true),
        reading = cell(false),
        pathPortals = cell<{ target: HTMLElement; value: string; dialog: HTMLElement }[]>([]),
        observations = cell<FileObservation[]>([]);
      let finder: Finder | undefined,
        manager: Awaited<ReturnType<typeof createFileManager>> | undefined,
        disposed = false,
        mountEpoch = 0;
      const initialPath = () =>
        props.path ?? "/";
      async function mount() {
        const epoch = ++mountEpoch;
        loading.value = true;
        try {
          await loadFinder();
          if (disposed || epoch !== mountEpoch || !host.value) return;
          const connection = await createFileManager(props.nodeId, (items) => {
            if (!disposed && epoch === mountEpoch) observations.value = items;
          }, (active) => { if (!disposed && epoch === mountEpoch) reading.value = active; });
          if (disposed || epoch !== mountEpoch) {
            connection.dispose();
            return;
          }
          manager = connection;
          const target = initialPath();
          finder = new window.elFinder(host.value, {
            url: "/elfinder-transport",
            transport: connection.transport(!!props.directoryPicker),
            lang: locale.value === "zh-CN" ? "zh_CN" : "en",
            startPathHash: connection.hash(target),
            // Fill the reserved dialog/flex box instead of a fixed pixel
            // height: the dialog opens at its final size and the manager
            // streams in without resizing it.
            height: "100%",
            width: "100%",
            defaultView: "list",
            resizable: false,
            cssAutoLoad: false,
            cssClass: "nodedance-elfinder",
            useBrowserHistory: false,
            rememberLastDir: false,
            sync: 0,
            soundPath: "",
            notifyDelay: 300,
            cdns: Object.fromEntries(
              Object.keys((window.elFinder as any).prototype._options.cdns).map(
                (key) => [key, false],
              ),
            ),
            ui: props.directoryPicker
              ? ["toolbar", "tree", "path", "stat"]
              : ["toolbar", "tree", "path", "stat"],
            commands: props.directoryPicker
              ? [
                  "open",
                  "reload",
                  "home",
                  "up",
                  "back",
                  "forward",
                  "mkdir",
                  "view",
                  "sort",
                  "info",
                ]
              : [
                  "open",
                  "reload",
                  "home",
                  "up",
                  "back",
                  "forward",
                  "mkdir",
                  "mkfile",
                  "upload",
                  "download",
                  "info",
                  "quicklook",
                  "copy",
                  "cut",
                  "paste",
                  "rm",
                  "rename",
                  "edit",
                  "archive",
                  "extract",
                  "search",
                  "view",
                  "sort",
                  "selectall",
                  "selectnone",
                  "selectinvert",
                  "fullscreen",
                  "help",
                ],
            uiOptions: {
              cwd: { replacement: { name: (file: FinderFile) => fileNameMarkup(file.name, file.mime === "directory") } },
              toolbar: props.directoryPicker
                ? [
                    ["back", "forward", "up"],
                    ["reload", "mkdir"],
                    ["view", "sort"],
                  ]
                : [
                    ["back", "forward", "up"],
                    ["reload"],
                    ["mkdir", "mkfile", "upload"],
                    ["download"],
                    ["copy", "cut", "paste"],
                    ["rename", "rm"],
                    ["edit", "quicklook", "info"],
                    ["archive", "extract"],
                    ["search"],
                    ["view", "sort", "fullscreen"],
                  ],
              tree: {
                openRootOnLoad: true,
                syncTree: true,
              },
            },
            commandsOptions: {
              open: {
                selectAction: "edit/quicklook",
              },
              edit: {
                mimes: [
                  "text/plain",
                  "text/csv",
                  "text/tab-separated-values",
                  "image/png",
                  "image/jpeg",
                  "image/webp",
                  "image/gif",
                  "image/bmp",
                ],
                extraOptions: {
                  tuiImgEditOpts: {
                    theme: {
                      "common.bi.image": "",
                      "common.backgroundColor": "var(--surface)",
                      "submenu.backgroundColor": "var(--surface)",
                      "submenu.normalLabel.color": "var(--muted)",
                      "submenu.activeLabel.color": "var(--text)",
                      "menu.normalIcon.color": "var(--muted)",
                      "menu.activeIcon.color": "var(--accent)",
                      "menu.disabledIcon.color": "var(--muted-strong)",
                      "menu.hoverIcon.color": "var(--text)",
                      "submenu.normalIcon.color": "var(--muted)",
                      "submenu.activeIcon.color": "var(--accent)",
                    },
                  },
                },
              },
              quicklook: {
                autoplay: false,
                googleDocsMimes: [],
                officeOnlineMimes: [],
                sharecadMimes: [],
              },
            },
            handlers: {
              dialogopen: (event: { data: { dialog: JQuery } }) => {
                // elFinder sets the content identity after creating its outer dialog.
                queueMicrotask(() => {
                  if (disposed || epoch !== mountEpoch) return;
                  const dialog = event.data.dialog?.[0];
                  if (!(dialog instanceof HTMLElement) || !dialog.isConnected || pathPortals.value.some(item => item.dialog === dialog)) return;
                  const content = dialog.querySelector<HTMLElement>(".ui-dialog-content[id*='-info-']");
                  const target = content?.querySelector<HTMLElement>(".elfinder-info-path");
                  if (!content || !target) return;
                  try {
                    const value = connection.path(content.id.slice(content.id.indexOf("-info-") + 6));
                    const slot = document.createElement("span");
                    // Native path selection may rewrite its value cell; keep the
                    // React copy control beside the native label instead.
                    (target.previousElementSibling ?? target).append(slot);
                    pathPortals.value = [...pathPortals.value, { target: slot, value, dialog }];
                  } catch { /* Multi-file information has no single path to copy. */ }
                });
              },
              dialogclosed: (event: { data: { dialog: JQuery } }) => { pathPortals.value = pathPortals.value.filter(item => item.dialog !== event.data.dialog?.[0]); },
              dialogremoved: (event: { data: { dialog: JQuery } }) => { pathPortals.value = pathPortals.value.filter(item => item.dialog !== event.data.dialog?.[0]); },
              open: () => {
                if (disposed || !finder) return;
                loading.value = false;
                const path = connection.path(finder.cwd().hash);
                if (props.directoryPicker) emit("directoryReady", path);
                else if (path !== (props.path ?? "/")) emit("pathChange", path);
              },
            },
            bootCallback: (
              fm: Finder & {
                openUrl: (
                  hash: string,
                  download?: boolean | unknown,
                  callback?: (url: string) => void,
                ) => string | void;
              },
            ) => {
              // elFinder's stock download/preview UI uses the exact discovered Core URL.
              // No connector query parameters, cache busters, or public filesystem URLs.
              fm.openUrl = (hash, download, callback) => {
                const url = download
                  ? connection.downloadURL(hash)
                  : (fm.file(hash)?.url ?? connection.downloadURL(hash));
                if (callback) callback(url);
                else return url;
              };
            },
          });
        } catch (reason) {
          if (!disposed && epoch === mountEpoch) error.value = String(reason);
        }
      }
      function destroy() {
        mountEpoch++;
        pathPortals.value = [];
        const currentFinder = finder, currentManager = manager;
        finder = undefined;
        manager = undefined;
        // Stop private requests before tearing down the third-party view.
        currentManager?.dispose();
        currentFinder?.destroy();
        if (props.directoryPicker) emit("directoryReady", "");
      }
      watchState(
        () => props.path,
        (value) => {
          if (
            finder &&
            manager &&
            !props.directoryPicker &&
            manager.path(finder.cwd().hash) !== (value ?? "/")
          )
            void finder.exec("open", manager.hash(value ?? "/"));
        },
      );
      watchState(locale, () => {
        destroy();
        void mount();
      });
      afterMount(() => void mount());
      beforeDispose(() => {
        disposed = true;
        destroy();
        manager?.dispose();
      });
      async function observe(id: string) {
        try {
          await manager?.observe(id);
        } catch (reason) {
          if (!disposed) error.value = String(reason);
        }
      }
      return () => (
        <>
          <section
            data-testid="node-files"
            aria-label={__props.nodeName as any}
            className={__classes("node-files ui-load-region")}
          >
            {error.value ? <p role="alert">{error.value}</p> : null}
            {(loading.value || reading.value) && !error.value ? <div className="file-load-feedback" data-testid="file-loading"><UiLoadState loading={loading.value || reading.value} ready={!loading.value} failed={!!error.value} /></div> : null}
            {pathPortals.value.map(item => createPortal(<UiPath value={item.value} copyOnly />, item.target, item.value))}
            <div
              ref={__dom(host)}
              aria-busy={loading.value}
              data-nodedance-files={__props.nodeId as any}
              className={__classes("elfinder-host")}
            ></div>
            {observations.value.length && !__props.directoryPicker ? (
              <section
                aria-label={t("host.history") as any}
                className={__classes("file-observations")}
              >
                <Fragment>
                  {__each(observations.value, (item) => (
                    <article key={item.taskId as any}>
                      <span>
                        <UiPath value={item.path} />
                        {" · "}
                        {item.status}
                      </span>
                      <button
                        type="button"
                        onClick={(event: any) => {
                          observe(item.taskId);
                        }}
                      >
                        {t("host.refresh")}
                      </button>
                      <button
                        type="button"
                        onClick={(event: any) => {
                          manager?.forget(item.taskId);
                        }}
                      >
                        {t("operations.forget")}
                      </button>
                    </article>
                  ))}
                </Fragment>
              </section>
            ) : null}
          </section>
        </>
      );
    },
    input,
    ["nodeId", "nodeName", "path", "directoryPicker"],
    [],
    {},
    true,
  );
}
