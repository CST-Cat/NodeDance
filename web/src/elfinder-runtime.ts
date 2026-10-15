import $ from "jquery";
import enMessages from "./locales/en.json";
import zhMessages from "./locales/zh-CN.json";
import "jquery-ui/themes/base/all.css";
import themeURL from "./elfinder.css?url";

export type FinderFile = {
  hash: string;
  phash?: string;
  name: string;
  mime: string;
  size: number;
  ts: number;
  read: number;
  write: number;
  locked?: number;
  dirs?: number;
  url?: string;
  volumeid?: string;
};
export interface Finder {
  cwd(): FinderFile;
  file(hash: string): FinderFile | undefined;
  exec(command: string, ...args: unknown[]): JQuery.Promise<unknown>;
  bind(event: string, listener: (event: { data: any }) => void): Finder;
  destroy(): void;
}
declare global {
  interface Window {
    jQuery: typeof $;
    $: typeof $;
    elFinder: new (
      element: HTMLElement,
      options: Record<string, unknown>,
    ) => Finder;
  }
}
let loaded: Promise<void> | undefined;
export function loadFinder() {
  if (!loaded)
    loaded = (async () => {
      window.jQuery = window.$ = $;
      await import("jquery-ui/ui/version");
      await import("jquery-ui/ui/widget");
      await import("jquery-ui/ui/data");
      await import("jquery-ui/ui/disable-selection");
      await import("jquery-ui/ui/focusable");
      await import("jquery-ui/ui/keycode");
      await import("jquery-ui/ui/plugin");
      await import("jquery-ui/ui/scroll-parent");
      await import("jquery-ui/ui/unique-id");
      await import("jquery-ui/ui/widgets/mouse");
      await import("jquery-ui/ui/widgets/draggable");
      await import("jquery-ui/ui/widgets/droppable");
      await import("jquery-ui/ui/widgets/resizable");
      await import("jquery-ui/ui/widgets/selectable");
      await import("jquery-ui/ui/widgets/button");
      await import("jquery-ui/ui/widgets/slider");
      await import("jquery-ui/ui/widgets/sortable");
      // External self-hosted scripts preserve Core's script-src CSP; no eval/CDN.
      for (const file of [
        "elfinder.js",
        "elfinder.zh_CN.js",
        "editors.default.js",
      ])
        await new Promise<void>((resolve, reject) => {
          const script = document.createElement("script");
          script.src = "/vendor/elfinder/" + file;
          script.onload = () => resolve();
          script.onerror = () => {
            script.remove();
            reject(Error("Unable to load elFinder"));
          };
          document.head.append(script);
        });
      const translations = (window.elFinder as any).prototype.i18;
      translations.en.messages.confirmRepl = enMessages["files.noOverwrite"];
      translations.zh_CN.messages.confirmRepl = zhMessages["files.noOverwrite"];
      const defaults = (window.elFinder as any).prototype._options
        .commandsOptions.edit;
      const image = defaults.editors.find(
        (editor: any) => editor.info?.id === "tuiimgedit",
      );
      if (image) {
        const load = image.load,
          setup = image.setup;
        image.setup = function (this: any, options: any, fm: any) {
          setup.call(this, options, fm);
          if (this.opts)
            this.opts.theme = options.extraOptions?.tuiImgEditOpts?.theme ?? {};
        };
        image.mimes = [
          "image/png",
          "image/jpeg",
          "image/webp",
          "image/gif",
          "image/bmp",
        ];
        image.load = function (this: any, base: HTMLElement) {
          const pending = $.Deferred();
          void loadImageEditor().then(
            (Editor) => {
              try {
                this.confObj.editor = Editor;
                load
                  .call(this, base)
                  .done((value: unknown) => {
                    pending.resolve(value);
                  })
                  .fail((reason: unknown) => {
                    pending.reject(reason);
                  });
              } catch (reason) {
                pending.reject([String(reason)]);
              }
            },
            (reason) => {
              pending.reject([String(reason)]);
            },
          );
          return pending;
        };
        defaults.editors = [
          image,
          defaults.editors.find(
            (editor: any) => editor.info?.id === "textarea",
          ),
        ];
      }
      for (const file of [
        "common",
        "dialog",
        "toolbar",
        "navbar",
        "statusbar",
        "contextmenu",
        "cwd",
        "quicklook",
        "commands",
        "toast",
        "places",
        "theme",
      ]) {
        const link = document.createElement("link");
        link.rel = "stylesheet";
        link.href = `/vendor/elfinder/css/${file}.css`;
        document.head.append(link);
      }
      const theme = document.createElement("link");
      theme.rel = "stylesheet";
      theme.href = themeURL;
      document.head.append(theme);
    })().catch((error) => {
      loaded = undefined;
      throw error;
    });
  return loaded;
}
export { $ };

let images: Promise<any> | undefined;
function loadImageEditor() {
  if (!images)
    images = (async () => {
      for (const file of [
        "tui-color-picker.min.css",
        "tui-image-editor.min.css",
      ]) {
        const link = document.createElement("link");
        link.rel = "stylesheet";
        link.href = "/vendor/file-editors/" + file;
        document.head.append(link);
      }
      for (const file of ["tui-color-picker.min.js", "tui-image-editor.min.js"])
        await new Promise<void>((resolve, reject) => {
          const script = document.createElement("script");
          script.src = "/vendor/file-editors/" + file;
          script.onload = () => resolve();
          script.onerror = () => {
            script.remove();
            reject(Error("Unable to load image editor"));
          };
          document.head.append(script);
        });
      return (window as any).tui.ImageEditor;
    })().catch((reason) => {
      images = undefined;
      throw reason;
    });
  return images;
}
