import { api, type TailscaleDeploymentTask, type TailscalePeer } from "./api";
import { t } from "./i18n";
import { afterMount, beforeDispose, cell, derived, observable } from "./state";
import type { AgentSetupDraft } from "./agent-enrollment";
export function createDiscoveryController(draft: AgentSetupDraft) {
  const peers = cell<TailscalePeer[]>([]);
  const loading = cell(false);
  const discovering = cell(true);
  const error = cell("");
  const notice = cell("");
  const selected = cell<TailscalePeer | null>(null);
  const fingerprint = cell("");
  const fingerprintLoading = cell(false);
  const task = cell<TailscaleDeploymentTask | null>(null);
  const unresolvedDeployments = cell<TailscaleDeploymentTask[]>([]);
  const activeDeploymentKey = cell("");
  const activeDeploymentSignature = cell("");
  let disposed = false;
  const resolutionOutcome = cell<"succeeded" | "failed">("succeeded");
  const resolutionObservedState = cell("connected");
  const resolutionConfirmed = cell(false);
  const credentials = observable({
    user: "",
    password: "",
    privateKey: "",
    passphrase: "",
  });
  const form = observable({
    fileRoot: "",
    disableFileRoot: false,
    fallbackUrl: "",
    allowFallback: false,
    confirmHostKey: false,
    confirmChangedHostKey: false,
    usePrivateKey: false,
  });
  const candidates = derived(() =>
    peers.value.filter((peer) => !peer.managed && peer.class === "linux"),
  );
  const unsupported = derived(() =>
    peers.value.filter((peer) => !peer.managed && peer.class !== "linux"),
  );
  const managed = derived(() => peers.value.filter((peer) => peer.managed));
  const taskFinished = derived(
    () =>
      !!task.value &&
      ["succeeded", "failed", "timed_out", "canceled", "unknown"].includes(
        task.value.status,
      ),
  );
  const rescueCommand = derived(() => {
    if (!selected.value?.ips.length) return "";
    const address = selected.value.ips[0];
    const host = address.includes(":") ? `[${address}]` : address;
    return t("m.9dee018adddc", {
      "0": shellQuote(
        t("m.6a82133ca245", {
          "0": credentials.user || t("m.a0b366e8b611"),
          "1": host,
        }),
      ),
    });
  });
  function peerLabel(peer: TailscalePeer): string {
    return peer.name || peer.dnsName || peer.identity;
  }
  function stateLabel(peer: TailscalePeer): string {
    return peer.online ? t("m.b9086662b1df") : t("m.be1b4f3c6c1c");
  }
  function clearNotice() {
    error.value = "";
    notice.value = "";
  }
  function changeResolutionOutcome(event: Event) {
    const value = (event.target as HTMLSelectElement).value;
    if (value !== "succeeded" && value !== "failed") return;
    resolutionConfirmed.value = false;
    resolutionOutcome.value = value;
    resolutionObservedState.value =
      value === "succeeded" ? "connected" : "installation_failed";
  }
  function changeResolutionObservedState(event: Event) {
    const value = (event.target as HTMLSelectElement).value;
    if (!["connected", "unavailable", "absent"].includes(value)) return;
    resolutionConfirmed.value = false;
    resolutionObservedState.value = value;
  }
  async function discover() {
    if (disposed) return;
    clearNotice();
    discovering.value = true;
    try {
      const result = await api.tailscalePeers();
      if (disposed) return;
      peers.value = result.peers;
      if (selected.value)
        selected.value =
          peers.value.find(
            (peer) => peer.identity === selected.value?.identity,
          ) ?? null;
      if (!peers.value.length) notice.value = t("m.8bcf5c4107b5");
    } catch (reason) {
      if (!disposed) error.value = message(reason);
    } finally {
      discovering.value = false;
    }
  }
  async function refreshDeploymentTasks() {
    if (disposed) return;
    try {
      const result = await api.tailscaleDeployments();
      if (disposed) return;
      unresolvedDeployments.value = result.tasks;
      if (task.value) {
        const current = result.tasks.find(
          (candidate) => candidate.taskId === task.value?.taskId,
        );
        if (current) task.value = current;
      } else {
        task.value = result.tasks[0] ?? null;
      }
    } catch (reason) {
      error.value = t("m.5b47ff2c5947", {
        "0": message(reason),
      });
    }
  }
  function inspectDeploymentTask(deployment: TailscaleDeploymentTask) {
    task.value = deployment;
    resolutionConfirmed.value = false;
  }
  function choose(peer: TailscalePeer) {
    clearNotice();
    const previousName = selected.value ? peerLabel(selected.value) : "";
    selected.value = peer;
    fingerprint.value = "";
    task.value = null;
    activeDeploymentKey.value = "";
    activeDeploymentSignature.value = "";
    resolutionConfirmed.value = false;
    credentials.password = "";
    credentials.privateKey = "";
    credentials.passphrase = "";
    if (!draft.displayName || draft.displayName === previousName)
      draft.displayName = peerLabel(peer);
    form.fileRoot = "";
    form.disableFileRoot = false;
    form.confirmHostKey = false;
    form.confirmChangedHostKey = false;
  }
  async function probeHostKey() {
    if (!selected.value) return;
    const peerIdentity = selected.value.identity;
    clearNotice();
    fingerprintLoading.value = true;
    try {
      const result = await api.probeTailscaleSSH(peerIdentity);
      if (disposed || selected.value?.identity !== peerIdentity) return;
      fingerprint.value = result.fingerprint;
    } catch (reason) {
      if (!disposed && selected.value?.identity === peerIdentity)
        error.value = message(reason);
    } finally {
      fingerprintLoading.value = false;
    }
  }
  async function startDeployment() {
    if (disposed || loading.value || !selected.value || !fingerprint.value)
      return;
    clearNotice();
    loading.value = true;
    const auth = form.usePrivateKey
      ? {
          user: credentials.user,
          privateKey: credentials.privateKey,
          passphrase: credentials.passphrase || undefined,
        }
      : {
          user: credentials.user,
          password: credentials.password,
        };
    const payload = {
      peerIdentity: selected.value.identity,
      displayName: draft.displayName,
      fileRoot: form.disableFileRoot
        ? undefined
        : form.fileRoot.trim() || undefined,
      disableFileRoot: form.disableFileRoot,
      coreUrl: draft.coreUrl,
      fallbackUrl: form.fallbackUrl || undefined,
      allowFallback: form.allowFallback,
      hostFingerprint: fingerprint.value,
      confirmHostKey: form.confirmHostKey,
      confirmChangedHostKey: form.confirmChangedHostKey,
      credentials: auth,
    };
    const signature = JSON.stringify({
      peerIdentity: payload.peerIdentity,
      displayName: payload.displayName,
      fileRoot: payload.fileRoot,
      disableFileRoot: payload.disableFileRoot,
      coreUrl: payload.coreUrl,
      fallbackUrl: payload.fallbackUrl,
      allowFallback: payload.allowFallback,
      hostFingerprint: payload.hostFingerprint,
      confirmChangedHostKey: payload.confirmChangedHostKey,
      sshUser: auth.user,
      authentication: form.usePrivateKey ? "private_key" : "password",
    });
    if (
      !activeDeploymentKey.value ||
      activeDeploymentSignature.value !== signature
    ) {
      activeDeploymentKey.value = crypto.randomUUID();
      activeDeploymentSignature.value = signature;
    }
    try {
      const accepted = await api.startTailscaleDeployment(
        payload,
        activeDeploymentKey.value,
      );
      if (disposed) return;
      task.value = accepted;
      unresolvedDeployments.value = [
        accepted,
        ...unresolvedDeployments.value.filter(
          (item) => item.taskId !== accepted.taskId,
        ),
      ];
      activeDeploymentKey.value = "";
      activeDeploymentSignature.value = "";
      credentials.password = "";
      credentials.privateKey = "";
      credentials.passphrase = "";
      await pollTask(accepted.taskId);
    } catch (reason) {
      error.value = message(reason);
    } finally {
      loading.value = false;
    }
  }
  async function resolveUnknownDeployment() {
    if (
      !task.value ||
      task.value.status !== "unknown" ||
      !resolutionConfirmed.value
    )
      return;
    clearNotice();
    try {
      task.value = await api.resolveTailscaleDeployment(task.value.taskId, {
        outcome: resolutionOutcome.value,
        observedState: resolutionObservedState.value,
      });
      resolutionConfirmed.value = false;
      await refreshDeploymentTasks();
      if (task.value.status === "succeeded") await discover();
    } catch (reason) {
      error.value = message(reason);
    }
  }
  async function pollTask(id: string) {
    for (let attempt = 0; attempt < 360; attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 1500));
      if (disposed || task.value?.taskId !== id) return;
      try {
        const current = await api.tailscaleDeployment(id);
        if (disposed || task.value?.taskId !== id) return;
        task.value = current;
      } catch (reason) {
        error.value = t("m.2eddbfa49d23", {
          "0": message(reason),
        });
        return;
      }
      if (taskFinished.value) {
        await refreshDeploymentTasks();
        if (task.value?.status === "succeeded") {
          const successMessage = task.value.message || t("m.37d815f92ccd");
          await discover();
          if (!error.value) notice.value = successMessage;
        } else if (task.value?.status === "unknown") {
          error.value = task.value.message || t("m.5ef75da06b52");
        } else {
          error.value = task.value?.message || t("m.b6b1127c181b");
        }
        return;
      }
    }
    error.value = t("m.e2de9b4ce02d");
  }
  function message(reason: unknown): string {
    return reason instanceof Error && reason.message
      ? reason.message
      : t("m.a8953ce415a5");
  }
  function shellQuote(value: string): string {
    return `'${value.replaceAll("'", "'\\''")}'`;
  }
  afterMount(() => {
    void discover();
    void refreshDeploymentTasks();
  });
  beforeDispose(() => {
    disposed = true;
    credentials.password = credentials.privateKey = credentials.passphrase = "";
  });
  return {
    peers,
    loading,
    discovering,
    error,
    notice,
    selected,
    fingerprint,
    fingerprintLoading,
    task,
    unresolvedDeployments,
    resolutionOutcome,
    resolutionObservedState,
    resolutionConfirmed,
    credentials,
    form,
    candidates,
    unsupported,
    managed,
    taskFinished,
    rescueCommand,
    peerLabel,
    stateLabel,
    discover,
    refreshDeploymentTasks,
    inspectDeploymentTask,
    choose,
    probeHostKey,
    startDeployment,
    resolveUnknownDeployment,
  };
}
export type AgentDiscovery = ReturnType<typeof createDiscoveryController>;
