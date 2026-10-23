import { SettingsGroup } from "./SettingsGroup";
import { t, display } from "../i18n";
import { useController } from "../state";
import {
  createAgentSetupDraft,
  createEnrollmentController,
} from "../agent-enrollment";
import { createDiscoveryController } from "../agent-discovery";
import {
  ManagementPage,
  Button,
  Input,
  Textarea,
  Checkbox,
  Alert,
  Select,
} from "./ManagementUI";
import SegmentedControl from "./arc/segmented-control/segmented-control";
import Accordion from "./arc/accordion/accordion";
import NodeAssetFields from "./NodeAssetFields";
import UiCopyFeedback from "./UiCopyFeedback";
import UiTaskProgress from "./UiTaskProgress";
import "./WorkspaceSettings.css";
import "./AgentOnboarding.css";

type Props = { onBack?: (event: any) => void };
export default function AgentOnboarding(props: Props) {
  return useController(
    () => {
      const draft = createAgentSetupDraft();
      const network = createDiscoveryController(draft);
      const enrollment = createEnrollmentController(
        {},
        draft,
        () => network.selected.value?.identity ?? "manual",
      );
      function manual() {
        network.selected.value = null;
        network.fingerprint.value = "";
        network.form.confirmHostKey =
          network.form.confirmChangedHostKey = false;
        network.credentials.password =
          network.credentials.privateKey =
          network.credentials.passphrase =
            "";
      }
      return () => {
        const peer = network.selected.value;
        const pending = peer
          ? network.loading.value
          : enrollment.creating.value;
        return (
          <ManagementPage
            registryStyles
            className="agent-onboarding compact-workspace"
            data-testid="agent-onboarding"
          >
            <header className="workspace-heading">
              <div>
                <h1>{t("nav.enrollment")}</h1>
              </div>
              <Button variant="ghost" onClick={props.onBack}>
                {t("m.e0e6dc51f626")}
              </Button>
            </header>
            <div className="agent-workbench">
              <aside
                className="workspace-section agent-targets"
                aria-labelledby="agent-targets-heading"
              >
                <div className="workspace-section-heading">
                  <h2 id="agent-targets-heading">{t("setup.target")}</h2>
                  <div className="workspace-actions">
                    {" "}
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={network.discovering.value}
                      onClick={() => void network.discover()}
                    >
                      {t("m.778b04988d4f")}
                    </Button>
                  </div>
                </div>
                <SettingsGroup className="target-picker">
                  <div className="target-row">
                    <strong>{t("setup.manualTarget")}</strong>
                    <Button
                      variant="ghost"
                      aria-label={t("setup.manualTarget")}
                      aria-pressed={!peer}
                      onClick={manual}
                    >
                      {!peer
                        ? t("setup.currentTarget")
                        : t("setup.selectTarget")}
                    </Button>
                  </div>
                  <div
                    className="network-targets"
                    data-testid="tailscale-discovery"
                  >
                    <h3>{t("setup.discoveredHosts")}</h3>
                    {network.discovering.value ? (
                      <p role="status">{t("m.41ac7e25e811")}</p>
                    ) : null}
                    {network.error.value && !peer ? (
                      <Alert
                        tone="danger"
                        title={t("setup.discoveryUnavailable")}
                      >
                        {display(network.error.value)}
                      </Alert>
                    ) : null}
                    {!network.discovering.value &&
                    !network.candidates.value.length &&
                    !network.error.value ? (
                      <p className="target-empty">{t("setup.noHosts")}</p>
                    ) : null}
                    {network.candidates.value.map((candidate) => (
                      <article
                        key={candidate.identity}
                        className="peer-card target-row"
                        data-identity={candidate.identity}
                      >
                        <div className="target-copy">
                          <strong>{network.peerLabel(candidate)}</strong>
                          <span>
                            {candidate.ips.join(", ") || candidate.os}
                          </span>
                        </div>
                        <Button
                          variant="ghost"
                          aria-label={network.peerLabel(candidate)}
                          aria-pressed={peer?.identity === candidate.identity}
                          onClick={() => network.choose(candidate)}
                        >
                          {peer?.identity === candidate.identity
                            ? t("setup.currentTarget")
                            : t("setup.selectTarget")}
                        </Button>
                      </article>
                    ))}
                    {network.managed.value.length ? (
                      <div className="target-secondary">
                        <h3>{t("setup.managedHosts")}</h3>
                        {network.managed.value.map((host) => (
                          <p key={host.identity}>
                            {network.peerLabel(host)}
                            {host.nodeId ? <code>{host.nodeId}</code> : null}
                          </p>
                        ))}
                      </div>
                    ) : null}
                    {network.unsupported.value.length ? (
                      <div className="target-secondary">
                        <h3>{t("setup.otherHosts")}</h3>
                        {network.unsupported.value.map((host) => (
                          <p key={host.identity}>
                            {network.peerLabel(host)} <span>{host.os}</span>
                          </p>
                        ))}
                      </div>
                    ) : null}
                  </div>
                </SettingsGroup>
              </aside>
              <section className="workspace-section agent-connection-section">
                <div className="workspace-section-heading">
                  <h2>{t("setup.connection")}</h2>
                </div>
                <form
                  className="agent-setup-form"
                  data-testid="agent-enrollment"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void (peer
                      ? network.startDeployment()
                      : enrollment.generateEnrollment());
                  }}
                >
                  <fieldset
                    disabled={!!peer && network.loading.value}
                    className="setup-fields"
                  >
                    <SettingsGroup className="setup-identity">
                      <div className="parameter-row">
                        <label htmlFor="agentonboarding-field-1">
                          {t("m.a09f47188571")}
                        </label>
                        <Input
                          id="agentonboarding-field-1"
                          name="displayName"
                          autoComplete="off"
                          maxLength={80}
                          required
                          value={draft.displayName}
                          onChange={(event) => {
                            draft.displayName = event.target.value;
                          }}
                        />
                      </div>
                      <div className="parameter-row">
                        <label htmlFor="agentonboarding-field-2">
                          {t("m.b66f55e3dc3d")}
                        </label>
                        <Input
                          id="agentonboarding-field-2"
                          name="coreUrl"
                          inputMode="url"
                          error={
                            !peer &&
                            (!enrollment.agentOriginIsSupported.value ||
                              !enrollment.agentOriginUsesAllowedTransport.value)
                              ? t("m.1d5964dbb8d8")
                              : undefined
                          }
                          autoComplete="url"
                          required
                          value={draft.coreUrl}
                          onChange={(event) => {
                            draft.coreUrl = event.target.value;
                          }}
                        />
                      </div>
                      {!peer ? (
                        <div
                          className="setup-deployment-method"
                          data-testid="agent-deployment-method"
                        >
                          <span>{t("agent.deploymentMethod")}</span>
                          <div className="parameter-control">
                            <SegmentedControl
                              label={t("agent.deploymentMethod")}
                              value={draft.deploymentMethod}
                              onValueChange={(value) => {
                                draft.deploymentMethod = value as
                                  "native" | "docker";
                              }}
                              options={[
                                {
                                  value: "native",
                                  label: t("setup.native"),
                                },
                                {
                                  value: "docker",
                                  label: t("setup.docker"),
                                },
                              ]}
                            />
                          </div>
                        </div>
                      ) : null}
                    </SettingsGroup>
                    {!peer ? (
                      <>
                        {enrollment.assetReady.value ? (
                          <section className="setup-assets">
                            <h3>{t("agent.assetsSection")}</h3>
                            <SettingsGroup>
                              <NodeAssetFields
                                showTitle={false}
                                showHints={false}
                                grouped
                                values={enrollment.asset.value}
                                schema={enrollment.assetSchema.value}
                                disabled={pending}
                              />
                            </SettingsGroup>
                          </section>
                        ) : (
                          <p role="status">{t("load.initial")}</p>
                        )}
                      </>
                    ) : (
                      <>
                        <SettingsGroup className="setup-ssh deploy-form">
                          <div className="setup-section-heading">
                            <h3>{t("setup.authentication")}</h3>
                            <span>{t("agent.nativeDeployment")}</span>
                          </div>
                          <div className="setup-identity">
                            <div className="parameter-row">
                              <label htmlFor="agentonboarding-field-3">
                                {t("m.4249329ab914")}
                              </label>
                              <Input
                                id="agentonboarding-field-3"
                                autoComplete="username"
                                required
                                value={network.credentials.user}
                                onChange={(event) => {
                                  network.credentials.user = event.target.value;
                                }}
                              />
                            </div>
                            <div className="parameter-row">
                              <span>{t("m.18d16b1edffd")}</span>
                              <Select
                                value={
                                  network.form.usePrivateKey
                                    ? "key"
                                    : "password"
                                }
                                onValueChange={(value) => {
                                  network.form.usePrivateKey = value === "key";
                                }}
                                options={[
                                  {
                                    value: "password",
                                    label: t("m.6f0890a1b0b0"),
                                  },
                                  { value: "key", label: t("m.2637e8166d92") },
                                ]}
                              />
                            </div>
                          </div>
                          {network.form.usePrivateKey ? (
                            <>
                              <div className="parameter-row">
                                <label htmlFor="agentonboarding-field-4">
                                  {t("m.2637e8166d92")}
                                </label>
                                <Textarea
                                  id="agentonboarding-field-4"
                                  rows={5}
                                  autoComplete="off"
                                  required
                                  value={network.credentials.privateKey}
                                  onChange={(event) => {
                                    network.credentials.privateKey =
                                      event.target.value;
                                  }}
                                />
                              </div>
                              <div className="parameter-row">
                                <label htmlFor="agentonboarding-field-5">
                                  {t("m.d2f245e82fd3")}
                                </label>
                                <Input
                                  id="agentonboarding-field-5"
                                  type="password"
                                  autoComplete="new-password"
                                  value={network.credentials.passphrase}
                                  onChange={(event) => {
                                    network.credentials.passphrase =
                                      event.target.value;
                                  }}
                                />
                              </div>
                            </>
                          ) : (
                            <div className="parameter-row">
                              <label htmlFor="agentonboarding-field-6">
                                {t("m.6f0890a1b0b0")}
                              </label>
                              <Input
                                id="agentonboarding-field-6"
                                type="password"
                                autoComplete="new-password"
                                value={network.credentials.password}
                                onChange={(event) => {
                                  network.credentials.password =
                                    event.target.value;
                                }}
                              />
                            </div>
                          )}
                        </SettingsGroup>
                        <section className="setup-verification hostkey-panel">
                          <div className="setup-section-heading">
                            <h3>{t("m.00487aceb17d")}</h3>
                            <Button
                              type="button"
                              variant="secondary"
                              className="secondary-button"
                              disabled={network.fingerprintLoading.value}
                              onClick={() => void network.probeHostKey()}
                            >
                              {network.fingerprintLoading.value
                                ? t("m.db728a839413")
                                : t("m.c2df7b3b46e0")}
                            </Button>
                          </div>
                          {network.fingerprint.value ? (
                            <>
                              <code
                                className="fingerprint"
                                data-testid="ssh-fingerprint"
                              >
                                {network.fingerprint.value}
                              </code>
                              <label className="check-row">
                                <Checkbox
                                  checked={network.form.confirmHostKey}
                                  onCheckedChange={(value) => {
                                    network.form.confirmHostKey =
                                      value === true;
                                  }}
                                />
                                {t("m.053515f793f4")}
                              </label>
                              <label className="check-row">
                                <Checkbox
                                  checked={network.form.confirmChangedHostKey}
                                  onCheckedChange={(value) => {
                                    network.form.confirmChangedHostKey =
                                      value === true;
                                  }}
                                />
                                {t("m.d643c97eee50")}
                              </label>
                            </>
                          ) : null}
                        </section>
                        <Accordion
                          defaultOpen={-1}
                          items={[
                            {
                              title: t("setup.advanced"),
                              content: (
                                <div className="setup-advanced">
                                  <div className="parameter-row">
                                    <label htmlFor="agentonboarding-field-7">
                                      {t("m.d62df2018d54")}
                                    </label>
                                    <Input
                                      id="agentonboarding-field-7"
                                      disabled={network.form.disableFileRoot}
                                      placeholder="/srv/nodedance-files"
                                      value={network.form.fileRoot}
                                      onChange={(event) => {
                                        network.form.fileRoot =
                                          event.target.value;
                                      }}
                                    />
                                  </div>
                                  <label className="check-row">
                                    <Checkbox
                                      checked={network.form.disableFileRoot}
                                      onCheckedChange={(value) => {
                                        network.form.disableFileRoot =
                                          value === true;
                                      }}
                                    />
                                    {t("m.63805b68bc8a")}
                                  </label>
                                  <div className="parameter-row">
                                    <label htmlFor="agentonboarding-field-8">
                                      {t("m.9ca13b13c082")}
                                    </label>
                                    <Input
                                      id="agentonboarding-field-8"
                                      value={network.form.fallbackUrl}
                                      onChange={(event) => {
                                        network.form.fallbackUrl =
                                          event.target.value;
                                      }}
                                    />
                                  </div>
                                  <label className="check-row">
                                    <Checkbox
                                      checked={network.form.allowFallback}
                                      onCheckedChange={(value) => {
                                        network.form.allowFallback =
                                          value === true;
                                      }}
                                    />
                                    {t("m.78c672bad4da")}
                                  </label>
                                  <div className="setup-rescue">
                                    <h3>{t("m.6e9f9850c332")}</h3>
                                    <code>{network.rescueCommand.value}</code>
                                    <code>
                                      systemctl status nodedance-agent
                                    </code>
                                  </div>
                                </div>
                              ),
                            },
                          ]}
                        />
                      </>
                    )}
                  </fieldset>
                  {(peer ? network.error.value : enrollment.error.value) ? (
                    <Alert
                      role="alert"
                      tone="danger"
                      title={t("management.error")}
                    >
                      {display(
                        peer ? network.error.value : enrollment.error.value,
                      )}
                    </Alert>
                  ) : null}
                  {peer && network.notice.value ? (
                    <Alert role="status" title={t("management.notice")}>
                      {network.notice.value}
                    </Alert>
                  ) : null}
                  <div className="setup-submit agent-enrollment-actions">
                    {!peer && enrollment.selectedInstallCommand.value ? (
                      <Button
                        type="button"
                        variant="secondary"
                        className="secondary-button"
                        aria-label={t("m.fe95745d1ef4")}
                        onClick={() =>
                          void enrollment.copied.copy(
                            enrollment.selectedInstallCommand.value,
                          )
                        }
                      >
                        <UiCopyFeedback state={enrollment.copied.state.value}>
                          {t("m.fe95745d1ef4")}
                        </UiCopyFeedback>
                      </Button>
                    ) : null}
                    <Button
                      type="submit"
                      className="primary-button"
                      disabled={
                        pending ||
                        (peer
                          ? !network.fingerprint.value ||
                            !network.form.confirmHostKey ||
                            !network.credentials.user ||
                            !draft.displayName
                          : !enrollment.assetReady.value ||
                            !draft.displayName.trim() ||
                            !enrollment.agentOriginIsSupported.value ||
                            !enrollment.agentOriginUsesAllowedTransport.value)
                      }
                    >
                      {pending
                        ? t("m.96461d04f477")
                        : peer
                          ? t("m.3795cc8670b9")
                          : t("m.92840286f8cd")}
                    </Button>
                  </div>
                </form>
              </section>
            </div>
            {network.unresolvedDeployments.value.length ? (
              <section
                className="workspace-section setup-activity"
                data-testid="unresolved-deployments"
              >
                <div className="setup-section-heading">
                  <h2>{t("m.756060e81582")}</h2>
                  <Button
                    variant="ghost"
                    onClick={() => void network.refreshDeploymentTasks()}
                  >
                    {t("m.e4c9549fe2f4")}
                  </Button>
                </div>
                {network.unresolvedDeployments.value.map((task) => (
                  <div className="deployment-record" key={task.taskId}>
                    <div className="deployment-identity">
                      <strong>{task.peerName || task.peerIdentity}</strong>
                      <code>{task.taskId}</code>
                    </div>
                    <UiTaskProgress
                      status={task.status}
                      phase={task.phase}
                      message={task.message}
                    />
                    <Button
                      variant="secondary"
                      onClick={() => network.inspectDeploymentTask(task)}
                    >
                      {task.status === "unknown"
                        ? t("m.196d981f3b71")
                        : t("m.36550b9c5748")}
                    </Button>
                  </div>
                ))}
              </section>
            ) : null}
            {network.task.value ? (
              <section
                className="workspace-section setup-activity"
                data-testid="deployment-task-section"
              >
                <div className="setup-section-heading">
                  <h2>{t("m.9343a9c6612f")}</h2>
                  <Button
                    variant="ghost"
                    onClick={() => {
                      network.task.value = null;
                    }}
                  >
                    {t("m.3fd47edce45b")}
                  </Button>
                </div>
                <div
                  data-testid="deployment-task"
                  className="task-status"
                  data-status={network.task.value.status}
                  role="status"
                >
                  <UiTaskProgress
                    status={network.task.value.status}
                    phase={network.task.value.phase}
                    message={network.task.value.message}
                  />
                  <code>{network.task.value.taskId}</code>
                  {network.task.value.nodeId ? (
                    <code>{network.task.value.nodeId}</code>
                  ) : null}
                </div>
                {network.task.value.status === "unknown" ? (
                  <div data-testid="deployment-review" className="setup-review">
                    <p>{t("m.e972e33c6e32")}</p>
                    <label className="parameter-row">
                      <span>{t("m.6e3bdd16a1ec")}</span>
                      <Select
                        value={network.resolutionOutcome.value}
                        onValueChange={(value) => {
                          network.resolutionConfirmed.value = false;
                          network.resolutionOutcome.value = value as
                            "succeeded" | "failed";
                          network.resolutionObservedState.value =
                            value === "succeeded"
                              ? "connected"
                              : "installation_failed";
                        }}
                        options={[
                          { value: "succeeded", label: t("m.18786701de8f") },
                          { value: "failed", label: t("m.981ea9952f00") },
                        ]}
                      />
                    </label>
                    {network.resolutionOutcome.value === "succeeded" ? (
                      <label className="parameter-row">
                        <span>{t("m.e0c379ab2eb0")}</span>
                        <Select
                          value={network.resolutionObservedState.value}
                          onValueChange={(value) => {
                            network.resolutionConfirmed.value = false;
                            network.resolutionObservedState.value = value;
                          }}
                          options={[
                            { value: "connected", label: t("m.83af47139246") },
                            {
                              value: "unavailable",
                              label: t("m.d3e431f9d10b"),
                            },
                            { value: "absent", label: t("m.8c4ecd0b9c16") },
                          ]}
                        />
                      </label>
                    ) : null}
                    <label className="check-row">
                      <Checkbox
                        checked={network.resolutionConfirmed.value}
                        onCheckedChange={(value) => {
                          network.resolutionConfirmed.value = value === true;
                        }}
                      />
                      {t("m.64aa1e37e12e")}
                    </label>
                    <Button
                      disabled={!network.resolutionConfirmed.value}
                      onClick={() => void network.resolveUnknownDeployment()}
                    >
                      {t("setup.resolveTask")}
                    </Button>
                  </div>
                ) : null}
              </section>
            ) : null}
          </ManagementPage>
        );
      };
    },
    props,
    ["onBack"],
    [],
    {},
    true,
  );
}
