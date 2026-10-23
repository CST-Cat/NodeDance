import { t } from "./i18n";
import $ from "jquery";
import type { FinderFile } from "./elfinder-runtime";
import {
  ApiError,
  operationRequest,
  type ContainerTask,
  type NodeFileEntry,
} from "./api";
import { operationCatalog, operationPath, exactJSON } from "./operations";
import { requestIntent, intentKey, finishIntent } from "./operation-intent";

export interface FileObservation {
  taskId: string;
  intent: string;
  path: string;
  status: string;
}
export async function createFileManager(
  nodeId: string,
  changed: (items: FileObservation[]) => void,
  activity?: (active: boolean) => void,
) {
  const catalog = await operationCatalog(),
    controller = new AbortController(),
    versions = new Map<string, string>();
  const maxFileSize = catalog.operations.find((o) => o.name === "files.upload")
    ?.upload?.maxBytes;
  const storage = `nodedance.file-observations.${nodeId}`;
  let observations: FileObservation[] = [];
  try {
    const saved = JSON.parse(sessionStorage.getItem(storage) ?? "[]");
    if (Array.isArray(saved))
      observations = saved
        .filter(
          (v) =>
            v &&
            typeof v.taskId === "string" &&
            typeof v.intent === "string" &&
            typeof v.path === "string" &&
            typeof v.status === "string",
        )
        .slice(-100);
  } catch {}
  const persist = () => {
    try {
      sessionStorage.setItem(storage, JSON.stringify(observations));
    } catch {}
    changed([...observations]);
  };
  const active = () => {
    controller.signal.throwIfAborted();
  };
  const signal = (other?: AbortSignal) =>
    other ? AbortSignal.any([controller.signal, other]) : controller.signal;
  const params = { nodeId };
  // Coalesce elFinder's overlapping open/tree/ls requests. Keep two read
  // slots so browsing cannot occupy every Core file-transfer slot.
  const pendingReads = new Map<string, Promise<unknown>>();
  const directories = new Map<string, { file: FinderFile; at: number }>();
  let reading = 0, pendingCount = 0;
  const waiting: (() => void)[] = [];
  const read = <T>(name: string, query: Record<string, string>, other?: AbortSignal): Promise<T> => {
    active();
    const key = JSON.stringify([name, Object.entries(query).sort()]);
    if (!other && pendingReads.has(key)) return pendingReads.get(key) as Promise<T>;
    pendingCount++;
    activity?.(true);
    const task = (async () => {
      if (reading >= 2) await new Promise<void>(resolve => waiting.push(resolve));
      else reading++;
      try {
        signal(other).throwIfAborted();
        return await operationRequest<T>(name, { params, query }, { signal: signal(other) });
      } finally {
        activity?.(--pendingCount > 0);
        const next = waiting.shift();
        if (next) next();
        else reading--;
      }
    })();
    if (!other) {
      pendingReads.set(key, task);
      void task.finally(() => { if (pendingReads.get(key) === task) pendingReads.delete(key); }).catch(() => {});
    }
    return task;
  };
  const url = (name: string, p: string) => {
    active();
    const operation = catalog.operations.find((o) => o.name === name);
    if (!operation) throw Error(`Missing Core operation: ${name}`);
    return operationPath(operation, { params, query: { path: p } });
  };
  const joined = (dir: string, name: string) =>
    dir.replace(/\/$/, "") + "/" + name;
  function hash(path: string) {
    return (
      "nd_" +
      btoa(String.fromCharCode(...new TextEncoder().encode(path)))
        .replace(/\+/g, "-")
        .replace(/\//g, "_")
        .replace(/=/g, "")
    );
  }
  function path(hashValue: unknown) {
    if (typeof hashValue !== "string" || !/^nd_[A-Za-z0-9_-]+$/.test(hashValue))
      throw Error("Invalid file identity");
    const value = new TextDecoder("utf-8", { fatal: true }).decode(
      Uint8Array.from(
        atob(hashValue.slice(3).replace(/-/g, "+").replace(/_/g, "/")),
        (c) => c.charCodeAt(0),
      ),
    );
    if (!value.startsWith("/") || hash(value) !== hashValue)
      throw Error("Invalid file identity");
    return value;
  }
  const parent = (value: string) =>
    value.slice(0, value.lastIndexOf("/")) || "/";
  function entry(value: NodeFileEntry): FinderFile {
    const directory = value.kind === "directory",
      regular = value.kind === "file",
      passive =
        /\.(png|jpe?g|gif|webp|bmp|avif|pdf|mp4|webm|mp3|ogg|wav)$/i.test(
          value.name,
        );
    const file: FinderFile = {
      hash: hash(value.path),
      ...(value.path === "/"
        ? { volumeid: "nd_" }
        : { phash: hash(parent(value.path)) }),
      name: value.path === "/" ? "/" : value.name,
      mime: directory ? "directory" : mime(value.name),
      size: value.size,
      ts: Math.floor(value.modifiedAt / 1e9),
      read: directory || regular ? 1 : 0,
      write: directory || regular ? 1 : 0,
      locked: directory || regular ? 0 : 1,
      ...(directory
        ? { dirs: 1 }
        : {
            url: url(passive ? "files.preview" : "files.download", value.path),
          }),
    };
    if (directory) {
      if (directories.size >= 512) directories.delete(directories.keys().next().value!);
      directories.set(value.path, { file, at: Date.now() });
    }
    return file;
  }
  async function pages(
    name: string,
    query: Record<string, string>,
    other?: AbortSignal,
  ) {
    const result: FinderFile[] = [];
    let after = "";
    do {
      const data = await read<{ entries: NodeFileEntry[]; nextAfter: string }>(
        name,
        { ...query, ...(after ? { after } : {}) },
        other,
      );
      active();
      for (const item of data.entries) result.push(entry(item));
      if (data.nextAfter === after && after)
        throw Error(t("files.invalidContinuation"));
      after = data.nextAfter;
      if (result.length > 100000) throw Error(t("files.directoryLimit"));
    } while (after);
    return result;
  }
  const stat = async (value: string, cached = false) => {
    const known = directories.get(value);
    if (cached && known && Date.now() - known.at < 10_000) return known.file;
    return entry(await read<NodeFileEntry>("files.stat", { path: value }));
  };
  const list = (value: string) => pages("files.list", { path: value });
  async function observe(item: FileObservation, other?: AbortSignal) {
    for (;;) {
      active();
      const task = await operationRequest<ContainerTask>(
        "tasks.get",
        { params: { ...params, taskId: item.taskId } },
        { signal: signal(other) },
      );
      active();
      item.status = task.status;
      persist();
      if (["queued", "running"].includes(task.status)) {
        await new Promise<void>((resolve, reject) => {
          const s = signal(other);
          const finish = () => {
            s.removeEventListener("abort", abort);
            resolve();
          };
          const timer = setTimeout(finish, 500);
          const abort = () => {
            clearTimeout(timer);
            s.removeEventListener("abort", abort);
            reject(s.reason);
          };
          s.addEventListener("abort", abort, { once: true });
          if (s.aborted) abort();
        });
        continue;
      }
      if (task.status === "unknown") throw Error(t("files.unknown"));
      directories.clear();
      finishIntent(item.intent);
      observations = observations.filter((v) => v !== item);
      persist();
      if (task.status !== "succeeded")
        throw Error(t("files.failed", { 0: task.result?.code || task.status }));
      return task;
    }
  }
  async function action(body: Record<string, unknown>, other?: AbortSignal) {
    active();
    directories.clear();
    const payload = exactJSON(body),
      intent = await requestIntent("files.action", { params }, payload);
    active();
    let item = observations.find((v) => v.intent === intent);
    if (!item) {
      if (observations.length >= 100) throw Error(t("files.observationLimit"));
      const accepted = await operationRequest<{
        taskId: string;
        status: string;
      }>(
        "files.action",
        { params },
        {
          headers: { "Idempotency-Key": intentKey(intent) },
          body: payload,
          signal: signal(other),
        },
      );
      active();
      item = {
        taskId: accepted.taskId,
        intent,
        path: String(body.path),
        status: accepted.status,
      };
      observations.push(item);
      persist();
    }
    await observe(item, other);
  }
  async function ensureUploadDirectory(
    path: string,
    other?: AbortSignal,
  ): Promise<void> {
    if (path === "/") return;
    try {
      const info = await read<NodeFileEntry>("files.stat", { path }, other);
      if (info.kind !== "directory") throw Error(t("files.notDirectory"));
      return;
    } catch (reason) {
      if (!(reason instanceof ApiError) || reason.status !== 404) throw reason;
    }
    await ensureUploadDirectory(
      path.slice(0, path.lastIndexOf("/")) || "/",
      other,
    );
    try {
      await action({ action: "mkdir", path }, other);
    } catch (reason) {
      const info = await read<NodeFileEntry>("files.stat", { path }, other);
      if (info.kind !== "directory") throw reason;
    }
  }
  async function upload(
    path: string,
    file: File,
    expectedVersion = "",
    other?: AbortSignal,
  ) {
    active();
    directories.clear();
    const query = { path },
      headers = {
        "Content-Type": "application/octet-stream",
        "X-File-Version": expectedVersion,
      };
    const intent = await requestIntent(
      "files.upload",
      { params, query },
      exactJSON({
        name: file.name,
        size: file.size,
        modifiedAt: file.lastModified,
        type: file.type,
        expectedVersion,
      }),
    );
    active();
    const pending = observations.find((item) => item.intent === intent);
    if (pending) return observe(pending, other);
    if (observations.length >= 100) throw Error(t("files.observationLimit"));
    const task = await operationRequest<ContainerTask>(
      "files.upload",
      { params, query },
      {
        headers: { ...headers, "Idempotency-Key": intentKey(intent) },
        body: file,
        signal: signal(other),
      },
      30 * 60_000,
    );
    active();
    if (task.status === "succeeded") {
      directories.clear();
      finishIntent(intent);
      return task;
    }
    const item = {
      taskId: task.taskId,
      intent,
      path: query.path,
      status: task.status,
    };
    observations.push(item);
    persist();
    return observe(item, other);
  }
  let cwd = "/";
  const disabled = [
    "netmount",
    "chmod",
    "resize",
    "zipdl",
    "url",
    "duplicate",
    "tmb",
    "dim",
    "subdirs",
    "callback",
    "editor",
  ];
  const options = {
    disabled,
    archivers: {
      create: ["application/zip", "application/x-tar", "application/x-gzip"],
      extract: [
        "application/zip",
        "application/x-tar",
        "application/x-gzip",
        "application/x-bzip2",
      ],
      createext: {
        "application/zip": "zip",
        "application/x-tar": "tar",
        "application/x-gzip": "tar.gz",
      },
    },
    uploadMaxSize: maxFileSize,
    uploadMaxConn: 1,
    separator: "/",
    copyOverwrite: false,
    uploadOverwrite: false,
    path: "/",
    url: "",
    tmbUrl: "",
  };
  async function open(target: string, directoryOnly: boolean) {
    const paths: string[] = [];
    for (let current = target; ; current = parent(current)) {
      paths.push(current);
      if (current === "/") break;
    }
    const [entries, ancestors] = await Promise.all([
      list(target), Promise.all(paths.map((p, index) => stat(p, index > 0))),
    ]);
    cwd = target;
    return {
      api: "2.1",
      cwd: ancestors[0],
      files: [
        ...ancestors,
        ...entries.filter(
          (item) => !directoryOnly || item.mime === "directory",
        ),
      ],
      options: {
        ...options,
        path: target,
        disabled: directoryOnly
          ? [
              ...disabled,
              "mkfile",
              "upload",
              "download",
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
            ]
          : disabled,
      },
      uplMaxSize: maxFileSize,
    };
  }
  async function request(
    data: Record<string, any>,
    directoryOnly = false,
  ): Promise<any> {
    active();
    const command = String(data.cmd),
      target = data.target ? path(data.target) : cwd;
    if (
      directoryOnly &&
      !["open", "tree", "parents", "mkdir", "ls"].includes(command)
    )
      throw Error(t("compose.directoryOnly"));
    switch (command) {
      case "open":
        return open(target, directoryOnly);
      case "tree":
        return {
          tree: (await list(target)).filter(
            (item) => item.mime === "directory",
          ),
        };
      case "parents": {
        const paths: string[] = [];
        for (let p = parent(target); ; p = parent(p)) {
          paths.push(p);
          if (p === "/") break;
        }
        return { tree: await Promise.all(paths.map(p => stat(p, true))) };
      }
      case "ls":
        return {
          list: Object.fromEntries(
            (await list(target)).map((item) => [item.hash, item.name]),
          ),
        };
      case "search":
        return {
          files: await pages("files.search", {
            path: data.target ? target : "/",
            filter: String(data.q ?? ""),
            recursive: "true",
          }),
        };
      case "mkdir":
      case "mkfile": {
        const destination = joined(target, String(data.name));
        await action(
          command === "mkdir"
            ? { action: "mkdir", path: destination }
            : { action: "save_text", path: destination, content: "" },
        );
        if (command === "mkfile") {
          const created = await read<{ text: string; version: string }>(
            "files.text",
            { path: destination },
          );
          active();
          if (created.text !== "") throw Error(t("files.reopen"));
          versions.set(destination, created.version);
        }
        return { added: [await stat(destination)] };
      }
      case "rename": {
        const destination = joined(parent(target), String(data.name));
        await action({ action: "rename", path: target, newPath: destination });
        versions.delete(target);
        return { removed: [data.target], added: [await stat(destination)] };
      }
      case "rm": {
        const removed: string[] = [];
        for (const value of data.targets ?? []) {
          const p = path(value);
          await action({
            action: "delete",
            path: p,
            deleteConfirmed: true,
            confirmationPath: p,
          });
          versions.delete(p);
          removed.push(value);
        }
        return { removed };
      }
      case "paste": {
        const added: FinderFile[] = [],
          removed: string[] = [];
        const destination = path(data.dst);
        for (const value of data.targets ?? []) {
          const source = path(value),
            newPath = joined(destination, source.split("/").pop()!);
          await action({
            action: data.cut ? "move" : "copy",
            path: source,
            newPath,
          });
          added.push(await stat(newPath));
          if (data.cut) removed.push(value);
        }
        return { added, removed };
      }
      case "archive": {
        const destination = joined(target, String(data.name));
        await action({
          action: "archive",
          path: destination,
          paths: (data.targets ?? []).map(path),
          ...(data.type
            ? {
                format: (
                  {
                    "application/zip": "zip",
                    "application/x-tar": "tar",
                    "application/x-gzip": "tar.gz",
                  } as Record<string, string>
                )[data.type],
              }
            : {}),
        });
        return { added: [await stat(destination)] };
      }
      case "extract": {
        let destination = parent(target);
        if (data.makedir) {
          destination = joined(
            destination,
            target
              .split("/")
              .pop()!
              .replace(/\.(tar\.gz|tar\.bz2|tbz2|tgz|zip|tar)$/i, ""),
          );
          await action({ action: "mkdir", path: destination });
        }
        const previous = new Map(
          data.makedir ? [] : (await list(destination)).map((file) => [file.hash, JSON.stringify(file)]),
        );
        await action({ action: "extract", path: target, newPath: destination });
        if (data.makedir) return { added: [await stat(destination)] };
        const extracted = await list(destination);
        return {
          added: extracted.filter((file) => !previous.has(file.hash)),
          changed: extracted.filter((file) => previous.has(file.hash) && previous.get(file.hash) !== JSON.stringify(file)),
        };
      }
      case "get": {
        if (/\.(png|jpe?g|webp|gif|bmp)$/i.test(target)) {
          const before = await read<NodeFileEntry>("files.stat", {
            path: target,
          });
          const blob = await operationRequest<Blob>(
            "files.download",
            { params, query: { path: target } },
            { signal: controller.signal },
          );
          const content = await new Promise<string>((resolve, reject) => {
            const reader = new FileReader();
            reader.onload = () => resolve(String(reader.result));
            reader.onerror = () => reject(reader.error);
            reader.readAsDataURL(new Blob([blob], { type: mime(target) }));
          });
          active();
          const after = await read<NodeFileEntry>("files.stat", {
            path: target,
          });
          if (!after.version || before.version !== after.version)
            throw Error(t("files.reopen"));
          versions.set(target, after.version);
          return { content, encoding: "UTF-8" };
        }
        const result = await read<{ text: string; version: string }>(
          "files.text",
          { path: target },
        );
        active();
        versions.set(target, result.version);
        return { content: result.text, encoding: "UTF-8" };
      }
      case "put": {
        const version = versions.get(target);
        if (!version) throw Error(t("files.reopen"));
        const content = String(data.content ?? "");
        if (/^data:image\/(png|jpeg|webp|gif|bmp);base64,/.test(content)) {
          const [header, base64] = content.split(",", 2),
            bytes = Uint8Array.from(atob(base64!), (c) => c.charCodeAt(0));
          await upload(
            target,
            new File([bytes], target.split("/").pop()!, {
              type: header!.slice(5, header!.indexOf(";")),
              lastModified: 0,
            }),
            version,
          );
          versions.delete(target);
          return { changed: [await stat(target)] };
        }
        await action({
          action: "save_text",
          path: target,
          content,
          expectedVersion: version,
        });
        versions.delete(target);
        const current = await read<{ text: string; version: string }>(
          "files.text",
          { path: target },
        );
        active();
        if (current.text !== content) throw Error(t("files.reopen"));
        versions.set(target, current.version);
        return { changed: [await stat(target)] };
      }
      case "info":
        return {
          files: await Promise.all(
            (data.targets ?? []).map((value: string) => stat(path(value))),
          ),
        };
      case "size":
        return { size: (await stat(target)).size };
      case "url":
        return { url: url("files.download", target) };
      default:
        throw Error("Unsupported file command: " + command);
    }
  }
  function deferred<T>(work: Promise<T>, abort?: () => void) {
    const value = $.Deferred<T>();
    void work.then(
      (result) => {
        value.resolve(result);
      },
      (reason) => {
        // elFinder expects the jQuery XHR rejection shape, not a string array.
        value.reject({
          status: reason instanceof ApiError ? reason.status : 0,
          responseText: JSON.stringify({ error: [String(reason instanceof Error ? reason.message : reason)] }),
        }, "error");
      },
    );
    return Object.assign(value.promise(), {
      abort: () => {
        abort?.();
        value.reject({ status: 0, quiet: true }, "abort");
      },
    });
  }
  const transport = (directoryOnly = false) => ({
    send: (options: { data: Record<string, any> }) =>
      deferred(request(options.data, directoryOnly)),
    upload: (
      data: {
        target?: string;
        input?: HTMLInputElement;
        files?: FileList | File[];
      },
      finder?: {
        updateCache: (data: unknown) => void;
        add: (data: unknown) => void;
        trigger: (event: string, data?: unknown) => void;
        error: (error: unknown) => void;
      },
    ) => {
      const transfer = new AbortController();
      return deferred(
        (async () => {
          if (directoryOnly) throw Error(t("compose.directoryOnly"));
          const added: FinderFile[] = [],
            failed: string[] = [];
          const destination = data.target ? path(data.target) : cwd;
          const files = Array.from(data.input?.files ?? data.files ?? []);
          for (const file of files) {
            if (!(file instanceof File))
              throw Error("Only local file uploads are supported");
            const relative = file.webkitRelativePath || file.name;
            const target = joined(destination, relative);
            try {
              await ensureUploadDirectory(parent(target), transfer.signal);
              await upload(target, file, "", transfer.signal);
              added.push(await stat(target));
            } catch (error) {
              if (controller.signal.aborted || transfer.signal.aborted)
                throw error;
              failed.push(String(error));
            }
          }
          const result = {
            added,
            ...(failed.length ? { warning: failed } : {}),
          };
          if (finder) {
            finder.updateCache(result);
            if (added.length) finder.add(result);
            if (failed.length) finder.error(failed);
            finder.trigger("upload", result);
            finder.trigger("uploaddone");
          }
          return result;
        })(),
        () => transfer.abort(),
      );
    },
  });
  persist();
  return {
    transport,
    request,
    hash,
    path,
    maxFileSize,
    downloadURL: (hashValue: string) => url("files.download", path(hashValue)),
    previewURL: (hashValue: string) => url("files.preview", path(hashValue)),
    dispose() {
      controller.abort();
      versions.clear();
      directories.clear();
      pendingReads.clear();
    },
    observations: () => [...observations],
    observe: async (id: string) => {
      const item = observations.find((v) => v.taskId === id);
      if (item) await observe(item);
    },
    forget(id: string) {
      observations = observations.filter((v) => v.taskId !== id);
      persist();
    },
  };
}

function mime(name: string) {
  const lower = name.toLowerCase();
  if (/\.(tar\.gz|tgz)$/.test(lower)) return "application/x-gzip";
  if (/\.(tar\.bz2|tbz2)$/.test(lower)) return "application/x-bzip2";
  const extension = lower.split(".").pop() ?? "";
  return (
    (
      {
        png: "image/png",
        jpg: "image/jpeg",
        jpeg: "image/jpeg",
        gif: "image/gif",
        webp: "image/webp",
        bmp: "image/bmp",
        avif: "image/avif",
        pdf: "application/pdf",
        mp4: "video/mp4",
        webm: "video/webm",
        mp3: "audio/mpeg",
        ogg: "audio/ogg",
        wav: "audio/wav",
        csv: "text/csv",
        tsv: "text/tab-separated-values",
        txt: "text/plain",
        svg: "text/plain",
        html: "text/plain",
        htm: "text/plain",
        zip: "application/zip",
        tar: "application/x-tar",
      } as Record<string, string>
    )[extension.toLowerCase()] ?? "text/plain"
  );
}
