import { api, operationRequest, type AgentNode } from "./api";
import type { GuestDisplay, GuestOptions, GuestSettings } from "./guest";
import { t } from "./i18n";
import { exactJSON } from "./operations";
import { afterMount, beforeDispose, cell, derived } from "./state";
export function createGuestPublication(canManage: () => boolean) {
  let epoch = 0;
  function reset() {
    epoch++;
    options.value = undefined;
    settings.value = undefined;
    preview.value = undefined;
    nodes.value = [];
    probes.value = [];
    error.value = "";
    notice.value = "";
    busy.value = false;
  }
  beforeDispose(() => {
    epoch++;
  });
  const options = cell<GuestOptions>(),
    settings = cell<GuestSettings>(),
    nodes = cell<AgentNode[]>([]),
    probes = cell<
      Array<{
        id: string;
        name: string;
      }>
    >([]),
    preview = cell<GuestDisplay>(),
    busy = cell(false),
    error = cell(""),
    notice = cell("");
  const nodeChoices = derived(() => [
    ...nodes.value,
    ...(settings.value?.nodes ?? [])
      .filter((r) => !nodes.value.some((n) => n.nodeId === r.id))
      .map((r) => ({
        nodeId: r.id,
        displayName: t("guest.missingResource", {
          "0": r.name,
        }),
      })),
  ]);
  const probeChoices = derived(() => [
    ...probes.value,
    ...(settings.value?.probes ?? [])
      .filter((r) => !probes.value.some((p) => p.id === r.id))
      .map((r) => ({
        id: r.id,
        name: t("guest.missingResource", {
          "0": r.name,
        }),
      })),
  ]);
  function selected(kind: "nodes" | "probes", id: string) {
    return settings.value?.[kind].find((r) => r.id === id);
  }
  function toggle(kind: "nodes" | "probes", id: string, checked: boolean) {
    if (!settings.value) return;
    settings.value[kind] = settings.value[kind].filter((r) => r.id !== id);
    if (checked)
      settings.value[kind].push({
        id,
        name: "",
      });
    preview.value = undefined;
  }
  function name(kind: "nodes" | "probes", id: string, value: string) {
    const resource = selected(kind, id);
    if (resource) resource.name = value;
    preview.value = undefined;
  }
  async function load() {
    if (!canManage()) return;
    const current = ++epoch;
    busy.value = true;
    error.value = "";
    try {
      const [config, list, services] = await Promise.all([
        operationRequest<GuestOptions>("guest.settings"),
        api.nodes(),
        operationRequest<{
          probes: Array<{
            id: string;
            name: string;
          }>;
        }>("probes.list"),
      ]);
      if (current !== epoch || !canManage()) return;
      options.value = config;
      settings.value = config.settings;
      nodes.value = list.nodes.filter(
        (n) => !["pending", "revoked"].includes(n.status),
      );
      probes.value = services.probes;
    } catch (e) {
      if (current === epoch) error.value = e instanceof Error ? e.message : String(e);
    } finally {
      if (current === epoch) busy.value = false;
    }
  }
  async function save() {
    const current = epoch;
    if (!settings.value) return;
    busy.value = true;
    error.value = "";
    notice.value = "";
    preview.value = undefined;
    try {
      const saved = await operationRequest<GuestSettings>(
        "guest.configure",
        {},
        {
          body: exactJSON(settings.value),
        },
      );
      if (current !== epoch) return;
      settings.value = saved;
      notice.value = t("guest.saved");
    } catch (e) {
      if (current === epoch) error.value = e instanceof Error ? e.message : String(e);
    } finally {
      if (current === epoch) busy.value = false;
    }
  }
  async function inspect() {
    const current = epoch;
    busy.value = true;
    error.value = "";
    try {
      const result = await operationRequest<GuestDisplay>("guest.preview");
      if (current === epoch) preview.value = result;
    } catch (e) {
      if (current === epoch) error.value = e instanceof Error ? e.message : String(e);
    } finally {
      if (current === epoch) busy.value = false;
    }
  }
  afterMount(() => {
    if (canManage()) void load();
  });
  return {
    reset,
    options,
    settings,
    preview,
    busy,
    error,
    notice,
    nodeChoices,
    probeChoices,
    selected,
    toggle,
    name,
    load,
    save,
    inspect,
  };
}
export type GuestPublication = ReturnType<typeof createGuestPublication>;
