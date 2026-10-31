import { t } from "./i18n";
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
  const pendingReads = new Map<string, Promise<unknown>>();
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
  async function pages(
    name: string,
    query: Record<string, string>,
    other?: AbortSignal,
  ) {
    const result: NodeFileEntry[] = [];
    let after = "";
    do {
      const data = await read<{ entries: NodeFileEntry[]; nextAfter: string }>(
        name,
        { ...query, ...(after ? { after } : {}) },
        other,
      );
      active();
      for (const item of data.entries) result.push(item);
      if (data.nextAfter === after && after)
        throw Error(t("files.invalidContinuation"));
      after = data.nextAfter;
      if (result.length > 100000) throw Error(t("files.directoryLimit"));
    } while (after);
    return result;
  }
  const stat = (path: string) => read<NodeFileEntry>("files.stat", { path });
  const list = (path: string) => pages("files.list", { path });
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
      finishIntent(item.intent);
      observations = observations.filter((v) => v !== item);
      persist();
      if (task.status !== "succeeded")
        throw Error(t("files.failed", { 0: task.result?.resourceRevision || task.result?.code || task.status }));
      return task;
    }
  }
  async function action(body: Record<string, unknown>, other?: AbortSignal) {
    active();
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
  let uploadTail: Promise<unknown> = Promise.resolve();
  function upload(path: string, file: File, expectedVersion = "", other?: AbortSignal) {
    const pending = uploadTail.catch(() => {}).then(() => { signal(other).throwIfAborted(); return sendUpload(path, file, expectedVersion, other); });
    uploadTail = pending.catch(() => {});
    return pending;
  }
  async function sendUpload(
    path: string,
    file: File,
    expectedVersion = "",
    other?: AbortSignal,
  ) {
    active();
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
  async function transfer(targetNodeId: string, paths: string[], targetPath: string, other?: AbortSignal) {
    active();
    const payload = exactJSON({ targetNodeId, paths, targetPath });
    const intent = await requestIntent("files.transfer", { params }, payload);
    active();
    const result = await operationRequest<{ transferred: { sourcePath: string; targetPath: string; size: number; sha256: string; taskId: string }[]; failed?: { sourcePath: string; error: string }[] }>(
      "files.transfer",
      { params },
      {
        headers: { "Idempotency-Key": intentKey(intent) },
        body: payload,
        signal: signal(other),
      },
      30 * 60_000,
    );
    finishIntent(intent);
    return result;
  }
  async function text(path: string) {
    const result = await read<{ text: string; version: string }>("files.text", { path });
    active();
    versions.set(path, result.version);
    return result.text;
  }
  async function saveText(path: string, content: string, source = path) {
    const expectedVersion = versions.get(source);
    if (path === source && !expectedVersion) throw Error(t("files.reopen"));
    await action({ action: "save_text", path, content, ...(path === source ? { expectedVersion } : {}) });
    versions.delete(path);
    const current = await text(path);
    if (current !== content) { versions.delete(path); throw Error(t("files.reopen")); }
  }
  async function image(path: string) {
    const before = await stat(path);
    const blob = await operationRequest<Blob>("files.download", { params, query: { path } }, { signal: controller.signal });
    const after = await stat(path);
    if (!after.version || after.version !== before.version) throw Error(t("files.reopen"));
    versions.set(path, after.version);
    return blob;
  }
  persist();
  return {
    list, stat, text, image, action, transfer,
    previewText: async (path: string, other?: AbortSignal) => (await read<{ text: string; version: string }>("files.text", { path }, other)).text,
    search: (path: string, filter: string, limits: Record<string, string> = {}, other?: AbortSignal) => pages("files.search", { path, filter, recursive: "true", ...limits }, other),
    saveText,
    saveImage: async (path: string, file: File, source = path) => {
      const expectedVersion = versions.get(source);
      if (path === source && !expectedVersion) throw Error(t("files.reopen"));
      await upload(path, file, path === source ? expectedVersion : "");
      versions.delete(path);
      const current = await image(path);
      const expected = new Uint8Array(await file.arrayBuffer()), actual = new Uint8Array(await current.arrayBuffer());
      if (expected.length !== actual.length || !expected.every((value, index) => value === actual[index])) {
        versions.delete(path); throw Error(t("files.reopen"));
      }
    },
    upload: async (destination: string, file: File, other?: AbortSignal) => {
      const path = joined(destination, file.webkitRelativePath || file.name);
      await ensureUploadDirectory(path.slice(0, path.lastIndexOf("/")) || "/", other);
      return upload(path, file, "", other);
    },
    downloadURL: (path: string) => url("files.download", path),
    previewURL: (path: string) => url("files.preview", path),
    maxFileSize,
    archiveFormats: catalog.operations.find(operation => operation.name === "files.action")?.bodySchema?.properties?.format?.enum?.map(String) ?? [],
    cancel: (taskId: string) => operationRequest("tasks.cancel", { params: { ...params, taskId } }),
    dispose() { controller.abort(); versions.clear(); pendingReads.clear(); },
    observations: () => [...observations],
    observe: async (id: string) => {
      const item = observations.find(v => v.taskId === id);
      if (item) return observe(item);
    },
    forget(id: string) { observations = observations.filter(v => v.taskId !== id); persist(); },
  };
}
export type FileManager = Awaited<ReturnType<typeof createFileManager>>;
export const fileParent = (path: string) => path.slice(0, path.lastIndexOf("/")) || "/";
export const fileJoin = (parent: string, name: string) => parent.replace(/\/$/, "") + "/" + name;
