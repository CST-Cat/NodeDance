import { AnimatePresence, motion } from "framer-motion";
import { SPRING_HEAVY_SNAP } from "./motion";
import { Fragment } from "react";
import {
  api,
  ApiError,
  operationRequest,
  SESSION_EXPIRED_EVENT,
  type Appearance,
  type Session,
  type User,
} from "./api";
import "./App.css";
import AgentEnrollment from "./components/AgentEnrollment";
import AlertCenter from "./components/AlertCenter";
import FeedsCenter from "./components/FeedsCenter";
import HostManagement from "./components/HostManagement";
import NodesDashboard from "./components/NodesDashboard";
import OperationConsole from "./components/OperationConsole";
import TailscaleDiscovery from "./components/TailscaleDiscovery";
import UiDialog from "./components/UiDialog";
import UiIcon from "./components/UiIcon";
import UiSelect from "./components/UiSelect";
import UpdateCenter from "./components/UpdateCenter";
import { cancelDialogs } from "./dialogs";
import { display, locale, setLocale, t, type Locale } from "./i18n";
import {
  navigationKey,
  nodeNavigationKey,
  readViewRoute,
  viewHash,
  writeViewRoute,
  type NodeNavigation,
  type ViewRoute,
  type ViewScreen,
} from "./navigation";
import { operationURL } from "./operations";
import {
  __call,
  __classes,
  __each,
  __input,
  __style,
  afterMount,
  afterRender,
  beforeDispose,
  cell,
  derived,
  lazyView,
  observable,
  provideValue,
  useController,
  watchState,
} from "./state";

const GuestDisplay = lazyView(() => import("./components/GuestDisplay"));
const GuestSettings = lazyView(() => import("./components/GuestSettings"));
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function App(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      type Screen = ViewScreen | "loading" | "unavailable";
      type ImageKind = "avatar" | "background";
      const screen = cell<Screen>("loading");
      watchState(screen, cancelDialogs);
      const navigationOpen = cell(false);
      const mobileNavigation = cell(false);
      const sidebarElement = cell<HTMLElement>();
      const sidebarCollapsed = cell(false);
      const sidebarWidth = cell(256);
      const sidebarResizing = cell(false);
      try {
        const saved = JSON.parse(
          localStorage.getItem("nodedance.sidebar") || "null",
        );
        if (saved) {
          sidebarCollapsed.value = saved.collapsed === true;
          if (Number.isFinite(saved.width))
            sidebarWidth.value = Math.max(200, Math.min(400, saved.width));
        }
      } catch {}
      watchState([sidebarCollapsed, sidebarWidth], () => {
        try {
          localStorage.setItem(
            "nodedance.sidebar",
            JSON.stringify({
              collapsed: sidebarCollapsed.value,
              width: sidebarWidth.value,
            }),
          );
        } catch {}
      });
      function toggleNavigation() {
        if (mobileNavigation.value)
          navigationOpen.value = !navigationOpen.value;
        else sidebarCollapsed.value = !sidebarCollapsed.value;
      }
      function resizeSidebar(event: PointerEvent) {
        if (
          mobileNavigation.value ||
          sidebarCollapsed.value ||
          event.button !== 0
        )
          return;
        const handle = event.currentTarget as HTMLElement;
        sidebarResizing.value = true;
        handle.setPointerCapture(event.pointerId);
        const start = event.clientX,
          width = sidebarWidth.value;
        // ArtifactsSplitPaneResizer behaviour: Escape mid-drag reverts the
        // pane width to the value captured at drag start.
        const cancel = (next: KeyboardEvent) => {
          if (next.key !== "Escape") return;
          next.preventDefault();
          sidebarWidth.value = width;
          stop();
          handle.focus();
        };
        const move = (next: PointerEvent) => {
          sidebarWidth.value = Math.max(
            200,
            Math.min(400, width + next.clientX - start),
          );
        };
        const stop = () => {
          sidebarResizing.value = false;
          window.removeEventListener("keydown", cancel);
          handle.removeEventListener("pointermove", move);
          handle.removeEventListener("lostpointercapture", stop);
        };
        window.addEventListener("keydown", cancel);
        handle.addEventListener("pointermove", move);
        handle.addEventListener("lostpointercapture", stop);
      }
      // Double-click snaps back to the untouched default pane width
      // (ArtifactsSplitPaneResizer's 50/50 reset, adapted to the sidebar's
      // 256px baseline).
      function resetSidebarWidth() {
        if (sidebarCollapsed.value || mobileNavigation.value) return;
        sidebarWidth.value = 256;
      }
      function resizeSidebarKeys(event: KeyboardEvent) {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
          return;
        event.preventDefault();
        sidebarWidth.value =
          event.key === "Home"
            ? 200
            : event.key === "End"
              ? 400
              : Math.max(
                  200,
                  Math.min(
                    400,
                    sidebarWidth.value +
                      (event.key === "ArrowRight" ? 10 : -10),
                  ),
                );
      }
      const navigationToggle = cell<HTMLButtonElement>();
      let colorScheme: MediaQueryList | undefined;
      let navigationQuery: MediaQueryList | undefined;
      function updateColorScheme(event: MediaQueryListEvent) {
        preferredTheme.value = event.matches ? "dark" : "light";
      }
      function updateNavigationViewport(event: MediaQueryListEvent) {
        mobileNavigation.value = event.matches;
        navigationOpen.value = false;
      }
      function handleNavigationKeys(event: KeyboardEvent) {
        if (!mobileNavigation.value || !navigationOpen.value) return;
        if (event.key === "Escape") {
          navigationOpen.value = false;
          navigationToggle.value?.focus();
        } else if (event.key === "Tab") {
          const items = sidebarElement.value?.querySelectorAll<HTMLElement>(
            "a, button:not(:disabled)",
          );
          if (!items?.length) return;
          const first = items[0]!,
            last = items[items.length - 1]!;
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
          }
        }
      }
      watchState(navigationOpen, async (open) => {
        if (open && mobileNavigation.value) {
          await afterRender();
          sidebarElement.value
            ?.querySelector<HTMLElement>("[aria-current=page]")
            ?.focus();
        }
      });
      const authenticated = derived(
        () =>
          !!currentUser.value &&
          !["guest", "loading", "unavailable", "setup", "login"].includes(
            screen.value,
          ),
      );
      const navigation = [
        {
          screen: "nodes",
          label: "nav.nodes",
          icon: "nodes",
        },
        {
          screen: "agent-enrollment",
          label: "nav.enrollment",
          icon: "add",
        },
        {
          screen: "discovery",
          label: "nav.discovery",
          icon: "discovery",
        },
        {
          screen: "alerts",
          label: "nav.alerts",
          icon: "alerts",
        },
        {
          screen: "host-management",
          label: "host.title",
          icon: "host",
        },
        {
          screen: "updates",
          label: "nav.updates",
          icon: "refresh",
        },
        {
          screen: "feeds",
          label: "feeds.title",
          icon: "subscriptions",
        },
        {
          screen: "operations",
          label: "nav.operations",
          icon: "operations",
        },
        {
          screen: "guest-settings",
          label: "guest.settings",
          icon: "guest",
        },
        {
          screen: "settings",
          label: "nav.settings",
          icon: "settings",
        },
      ] as const;
      const activeRoute = cell<ViewRoute>(readViewRoute());
      const nodeNavigation = cell<NodeNavigation | null>(null);
      provideValue(nodeNavigationKey, nodeNavigation);
      const detailNavigation = derived(() =>
        screen.value === "nodes" &&
        activeRoute.value.nodeId === nodeNavigation.value?.nodeId
          ? nodeNavigation.value
          : null,
      );
      const screenTitle = derived(
        () =>
          detailNavigation.value?.sections.find(
            (item) => item.id === detailNavigation.value?.activeSection,
          )?.label ??
          t(
            navigation.find((item) => item.screen === screen.value)?.label ??
              "nav.workspace",
          ),
      );
      function navigateNodeSection(section: string) {
        if (detailNavigation.value)
          goToRoute({
            screen: "nodes",
            nodeId: detailNavigation.value.nodeId,
            section,
          });
      }
      let routeSequence = 0;
      let handledLocation = "";
      let loginDestination: ViewRoute = {
        screen: "nodes",
      };
      provideValue(navigationKey, {
        route: activeRoute,
        go: goToRoute,
      });
      function displayRoute(route: ViewRoute) {
        activeRoute.value = route;
        screen.value = route.screen;
        navigationOpen.value = false;
        writeViewRoute(route, true);
        handledLocation = window.location.href;
        void afterRender(() => window.scrollTo(0, 0));
      }
      function goToRoute(route: ViewRoute, replace = false) {
        if (
          window.location.hash === viewHash(route) &&
          screen.value === route.screen
        )
          return;
        writeViewRoute(route, replace);
        handledLocation = window.location.href;
        void activateRoute(readViewRoute());
      }
      function navigate(target: ViewScreen, replace = false) {
        goToRoute(
          {
            screen: target,
          },
          replace,
        );
      }
      function handleHistoryPageShow(event: PageTransitionEvent) {
        if (!event.persisted) return;
        currentUser.value = null;
        sessions.value = [];
        loginPassword.value = "";
        setupForm.password = "";
        setupForm.credential = "";
        passwordForm.currentPassword = "";
        passwordForm.newPassword = "";
        passwordForm.confirmPassword = "";
        handledLocation = window.location.href;
        void activateRoute(readViewRoute());
      }
      function handleHistoryNavigation() {
        if (handledLocation === window.location.href) return;
        handledLocation = window.location.href;
        void activateRoute(readViewRoute());
      }
      const busy = cell(false);
      const busyLabel = cell("");
      const error = cell("");
      const notice = cell("");
      const guestEnabled = cell(false);
      watchState(screen, async (current) => {
        if (current !== "login") return;
        guestEnabled.value = false;
        try {
          guestEnabled.value = (
            await operationRequest<{
              enabled: boolean;
            }>("guest.entry")
          ).enabled;
        } catch {
          guestEnabled.value = false;
        }
      });
      const initialized = cell(false);
      const currentUser = cell<User | null>(null);
      const sessions = cell<Session[]>([]);
      const uploading = cell<ImageKind | "">("");
      const preferredTheme = cell<"dark" | "light">(
        window.matchMedia("(prefers-color-scheme: dark)").matches
          ? "dark"
          : "light",
      );
      const avatarRevision = cell(0);
      const backgroundRevision = cell(0);
      const setupForm = observable({
        credential: "",
        password: "",
        username: "",
      });
      const initialCredentials = cell<{
        username: string;
        password?: string;
      } | null>(null);
      const loginUsername = cell("");
      const loginPassword = cell("");
      const passwordForm = observable({
        currentPassword: "",
        newPassword: "",
        confirmPassword: "",
      });
      const appearance = observable<Appearance>({
        displayName: "",
        theme:
          (document.documentElement.dataset
            .themePreference as Appearance["theme"]) || "system",
        backgroundColor: "#faf9f5",
        avatarUrl: "",
        backgroundUrl: "",
      });
      const safeBackgroundColor = derived(() =>
        /^#[\da-f]{6}$/i.test(appearance.backgroundColor)
          ? appearance.backgroundColor
          : "var(--canvas)",
      );
      const shellStyle = derived(() => ({
        "--page-color": safeBackgroundColor.value,
        "--sidebar-width": `${sidebarCollapsed.value && !mobileNavigation.value ? 52 : sidebarWidth.value}px`,
      }));
      const effectiveTheme = derived(() =>
        appearance.theme === "system" ? preferredTheme.value : appearance.theme,
      );
      watchState(
        effectiveTheme,
        (theme) => {
          document.documentElement.dataset.theme = theme;
          document.documentElement.style.colorScheme = theme;
          document.documentElement.style.backgroundColor =
            theme === "dark" ? "#141413" : "#faf9f5";
          document
            .querySelector("meta[name=theme-color]")
            ?.setAttribute("content", theme === "dark" ? "#141413" : "#faf9f5");
        },
        {
          immediate: true,
        },
      );
      const imagePaths = observable({
        avatar: "",
        background: "",
      });
      function imageUrl(value: string, kind: ImageKind): string {
        if (!value) return "";
        try {
          const url = new URL(value, window.location.origin);
          const expectedPath = imagePaths[kind];
          if (
            url.origin !== window.location.origin ||
            url.pathname !== expectedPath
          )
            return "";
          return url.pathname;
        } catch {
          return "";
        }
      }
      function revisionedImageURL(
        value: string,
        kind: ImageKind,
        revision: number,
      ): string {
        const path = imageUrl(value, kind);
        return path && revision > 0 ? `${path}?rev=${revision}` : path;
      }
      const avatarSrc = derived(() =>
        revisionedImageURL(
          appearance.avatarUrl,
          "avatar",
          avatarRevision.value,
        ),
      );
      const backgroundSrc = derived(() =>
        revisionedImageURL(
          appearance.backgroundUrl,
          "background",
          backgroundRevision.value,
        ),
      );
      const visibleName = derived(
        () =>
          appearance.displayName ||
          currentUser.value?.displayName ||
          t("m.e19796712f1c"),
      );
      function clearMessages() {
        error.value = "";
        notice.value = "";
      }
      function errorText(reason: unknown): string {
        if (reason instanceof Error && reason.message) return reason.message;
        return t("m.2c1e5ec62145");
      }
      function rememberTheme() {
        try {
          localStorage.setItem("nodedance.theme", appearance.theme);
        } catch {}
      }
      async function loadAppearance() {
        const [result, avatar, background] = await Promise.all([
          api.appearance(),
          operationURL("appearance.avatar"),
          operationURL("appearance.background"),
        ]);
        imagePaths.avatar = avatar.pathname;
        imagePaths.background = background.pathname;
        Object.assign(appearance, result);
        if (!/^#[\da-f]{6}$/i.test(appearance.backgroundColor)) {
          appearance.backgroundColor = "#101827";
        }
        if (
          appearance.theme !== "light" &&
          appearance.theme !== "dark" &&
          appearance.theme !== "system"
        )
          appearance.theme = "system";
        rememberTheme();
      }
      async function loadSessions() {
        const result = await api.sessions();
        sessions.value = result.sessions;
      }
      async function enterSettings(sequence = routeSequence) {
        const result = await api.me();
        if (sequence !== routeSequence) return false;
        currentUser.value = result.user;
        const results = await Promise.allSettled([
          loadAppearance(),
          loadSessions(),
        ]);
        const rejected = results.find((item) => item.status === "rejected");
        if (sequence !== routeSequence) return false;
        if (rejected?.status === "rejected") {
          notice.value = t("m.2ccd4dc3ec31");
        }
        return true;
      }
      async function activateRoute(target: ViewRoute) {
        const sequence = ++routeSequence;
        navigationOpen.value = false;
        busy.value = false;
        busyLabel.value = "";
        uploading.value = "";
        cancelDialogs();
        clearMessages();
        if (target.screen !== "login") loginPassword.value = "";
        if (target.screen !== "setup") {
          setupForm.password = "";
          setupForm.credential = "";
        }
        if (target.screen === "guest") {
          currentUser.value = null;
          sessions.value = [];
          displayRoute({
            screen: "guest",
          });
          return;
        }
        if (!currentUser.value) screen.value = "loading";
        try {
          if (!initialized.value) {
            const state = await api.setupStatus();
            if (sequence !== routeSequence) return;
            initialized.value = state.initialized;
            await loadAppearance();
            if (sequence !== routeSequence) return;
          }
          if (!initialized.value) {
            displayRoute({
              screen: "setup",
            });
            return;
          }
          if (target.screen === "login") {
            currentUser.value = null;
            sessions.value = [];
            displayRoute({
              screen: "login",
            });
            return;
          }
          const result = await api.me();
          if (sequence !== routeSequence) return;
          currentUser.value = result.user;
          if (target.screen === "settings") {
            const results = await Promise.allSettled([
              loadAppearance(),
              loadSessions(),
            ]);
            if (sequence !== routeSequence) return;
            if (results.some((item) => item.status === "rejected"))
              notice.value = t("m.2ccd4dc3ec31");
          }
          displayRoute(
            target.screen === "setup"
              ? {
                  screen: "nodes",
                }
              : target,
          );
        } catch (reason) {
          if (sequence !== routeSequence) return;
          if (reason instanceof ApiError && reason.status === 401) {
            currentUser.value = null;
            sessions.value = [];
            loginDestination = target;
            displayRoute({
              screen: "login",
            });
            return;
          }
          error.value = errorText(reason);
          screen.value = "unavailable";
        }
      }
      async function initializeScreen() {
        await activateRoute(readViewRoute());
      }
      async function submitSetup() {
        const sequence = routeSequence;
        clearMessages();
        busy.value = true;
        busyLabel.value = t("m.9866f869b0bd");
        try {
          const result = await api.setup({
            credential: setupForm.credential,
            password: setupForm.password,
            username: setupForm.username,
          });
          if (sequence !== routeSequence) return;
          initialCredentials.value = result.initialCredentials ?? null;
          initialized.value = true;
          try {
            if (!(await enterSettings(sequence))) return;
          } catch (reason) {
            if (!(reason instanceof ApiError) || reason.status !== 401)
              throw reason;
            await api.login(
              result.initialCredentials?.username ?? setupForm.username,
              result.initialCredentials?.password ?? setupForm.password,
            );
            if (!(await enterSettings(sequence))) return;
          }
          initialCredentials.value = result.initialCredentials ?? null;
          navigate("nodes", true);
          setupForm.credential = "";
          setupForm.password = "";
          setupForm.username = "";
          notice.value = t("m.ea48349b4814");
        } catch (reason) {
          if (sequence === routeSequence) error.value = errorText(reason);
        } finally {
          if (sequence === routeSequence) {
            busy.value = false;
            busyLabel.value = "";
          }
        }
      }
      async function submitLogin() {
        const sequence = routeSequence;
        clearMessages();
        busy.value = true;
        busyLabel.value = t("m.d8712b0d82e6");
        try {
          await api.login(loginUsername.value, loginPassword.value);
          if (sequence !== routeSequence || !(await enterSettings(sequence)))
            return;
          goToRoute(loginDestination, true);
          loginPassword.value = "";
        } catch (reason) {
          if (sequence === routeSequence) error.value = errorText(reason);
        } finally {
          if (sequence === routeSequence) {
            busy.value = false;
            busyLabel.value = "";
          }
        }
      }
      async function saveProfile() {
        clearMessages();
        busy.value = true;
        busyLabel.value = t("m.6bdb4435095e");
        try {
          await api.saveAppearance({
            displayName: appearance.displayName,
            theme: appearance.theme,
            backgroundColor: appearance.backgroundColor,
          });
          rememberTheme();
          currentUser.value = {
            displayName: appearance.displayName,
          };
          notice.value = t("m.e90110b8adca");
        } catch (reason) {
          error.value = errorText(reason);
        } finally {
          busy.value = false;
          busyLabel.value = "";
        }
      }
      async function handleImageChange(kind: ImageKind, event: Event) {
        clearMessages();
        const input = event.target as HTMLInputElement;
        const image = input.files?.[0];
        if (!image) return;
        input.value = "";
        const acceptedTypes = new Set(["image/png", "image/jpeg", "image/gif"]);
        if (
          !acceptedTypes.has(image.type.toLowerCase()) ||
          /\.svgz?$/i.test(image.name)
        ) {
          error.value = t("m.37492576162f");
          return;
        }
        uploading.value = kind;
        try {
          await api.uploadImage(kind, image);
          if (kind === "avatar") avatarRevision.value += 1;
          else backgroundRevision.value += 1;
          await loadAppearance();
          notice.value =
            kind === "avatar" ? t("m.38e48df5ee98") : t("m.4e208b8c6edc");
        } catch (reason) {
          error.value = errorText(reason);
        } finally {
          uploading.value = "";
        }
      }
      async function revokeSession(session: Session) {
        if (session.current) return;
        clearMessages();
        busy.value = true;
        try {
          await api.revokeSession(session.id);
          sessions.value = sessions.value.filter(
            (item) => item.id !== session.id,
          );
          notice.value = t("m.2f372ae7fc9d");
        } catch (reason) {
          error.value = errorText(reason);
        } finally {
          busy.value = false;
        }
      }
      async function changePassword() {
        const sequence = routeSequence;
        clearMessages();
        if (passwordForm.newPassword !== passwordForm.confirmPassword) {
          error.value = t("m.a2ba2545a60e");
          return;
        }
        busy.value = true;
        busyLabel.value = t("m.2c62c5fc337d");
        try {
          await api.changePassword(
            passwordForm.currentPassword,
            passwordForm.newPassword,
          );
          if (sequence !== routeSequence) {
            handleSessionExpired();
            return;
          }
          currentUser.value = null;
          initialCredentials.value = null;
          sessions.value = [];
          loginPassword.value = "";
          setupForm.password = "";
          passwordForm.currentPassword = "";
          passwordForm.newPassword = "";
          passwordForm.confirmPassword = "";
          loginDestination = {
            screen: "nodes",
          };
          navigate("login", true);
          notice.value = t("m.37a88dd4c7a5");
        } catch (reason) {
          error.value = errorText(reason);
        } finally {
          busy.value = false;
          busyLabel.value = "";
        }
      }
      async function logout() {
        const sequence = routeSequence;
        clearMessages();
        busy.value = true;
        try {
          await api.logout();
          if (sequence !== routeSequence) {
            handleSessionExpired();
            return;
          }
          currentUser.value = null;
          initialCredentials.value = null;
          sessions.value = [];
          loginDestination = {
            screen: "nodes",
          };
          navigate("login", true);
          notice.value = t("m.3a7b6d2f0e58");
        } catch (reason) {
          error.value = errorText(reason);
        } finally {
          busy.value = false;
        }
      }
      function handleSessionExpired() {
        if (screen.value === "guest") {
          currentUser.value = null;
          sessions.value = [];
          return;
        }
        if (!currentUser.value) return;
        initialCredentials.value = null;
        currentUser.value = null;
        sessions.value = [];
        loginPassword.value = "";
        setupForm.password = "";
        passwordForm.currentPassword = "";
        passwordForm.newPassword = "";
        passwordForm.confirmPassword = "";
        busy.value = false;
        busyLabel.value = "";
        uploading.value = "";
        clearMessages();
        const destination = readViewRoute();
        if (!["login", "guest", "setup"].includes(destination.screen))
          loginDestination = destination;
        routeSequence++;
        displayRoute({
          screen: "login",
        });
        notice.value = t("m.bd0bb959fbb4");
      }
      function formatTime(value: string): string {
        const time = new Date(value);
        return Number.isNaN(time.getTime())
          ? "—"
          : time.toLocaleString(locale.value);
      }
      afterMount(() => {
        window.addEventListener(SESSION_EXPIRED_EVENT, handleSessionExpired);
        colorScheme = window.matchMedia("(prefers-color-scheme: dark)");
        preferredTheme.value = colorScheme.matches ? "dark" : "light";
        colorScheme.addEventListener("change", updateColorScheme);
        navigationQuery = window.matchMedia("(max-width: 900px)");
        mobileNavigation.value = navigationQuery.matches;
        navigationQuery.addEventListener("change", updateNavigationViewport);
        window.addEventListener("keydown", handleNavigationKeys);
        window.addEventListener("pageshow", handleHistoryPageShow);
        window.addEventListener("popstate", handleHistoryNavigation);
        window.addEventListener("hashchange", handleHistoryNavigation);
        handledLocation = window.location.href;
        void initializeScreen();
      });
      beforeDispose(() => {
        window.removeEventListener(SESSION_EXPIRED_EVENT, handleSessionExpired);
        window.removeEventListener("keydown", handleNavigationKeys);
        window.removeEventListener("pageshow", handleHistoryPageShow);
        window.removeEventListener("popstate", handleHistoryNavigation);
        window.removeEventListener("hashchange", handleHistoryNavigation);
        routeSequence++;
        colorScheme?.removeEventListener("change", updateColorScheme);
        navigationQuery?.removeEventListener(
          "change",
          updateNavigationViewport,
        );
      });
      return () => (
        <>
          <main
            data-theme={effectiveTheme.value as any}
            data-screen={screen.value as any}
            data-authenticated={authenticated.value as any}
            data-sidebar-resizing={sidebarResizing.value as any}
            className={__classes("app-shell")}
            style={__style(shellStyle.value)}
            data-v-ac863f34=""
          >
            {screen.value === "login" && backgroundSrc.value ? (
              <div
                aria-hidden="true"
                className={__classes("login-background")}
                data-v-ac863f34=""
              >
                <img
                  key={backgroundRevision.value as any}
                  src={backgroundSrc.value as any}
                  alt=""
                  data-v-ac863f34=""
                ></img>
                <div
                  className={__classes("login-background-shade")}
                  data-v-ac863f34=""
                ></div>
              </div>
            ) : null}
            {authenticated.value && navigationOpen.value ? (
              <button
                type="button"
                aria-label={t("nav.close") as any}
                className={__classes("navigation-scrim")}
                onClick={(event: any) => {
                  navigationOpen.value = false;
                }}
                data-v-ac863f34=""
              ></button>
            ) : null}
            {authenticated.value ? (
              <aside
                id="workspace-navigation"
                ref={__dom(sidebarElement)}
                inert={mobileNavigation.value && (!navigationOpen.value as any)}
                className={__classes("workspace-sidebar", {
                  "is-open": navigationOpen.value,
                  "is-collapsed":
                    sidebarCollapsed.value && !mobileNavigation.value,
                })}
                data-v-ac863f34=""
              >
                <a
                  href="/"
                  aria-label={t("m.f359facd4191") as any}
                  className={__classes("wordmark")}
                  data-v-ac863f34=""
                >
                  <span
                    aria-hidden="true"
                    className={__classes("brand-mark")}
                    data-v-ac863f34=""
                  >
                    <i data-v-ac863f34=""></i>
                    <i data-v-ac863f34=""></i>
                    <i data-v-ac863f34=""></i>
                  </span>
                  <span data-v-ac863f34="">{"NodeDance"}</span>
                </a>
                <AnimatePresence mode="wait">
                  <motion.div
                    key={detailNavigation.value?.nodeId || ("workspace" as any)}
                    data-context={
                      detailNavigation.value ? "node" : ("workspace" as any)
                    }
                    className={__classes("sidebar-context")}
                    data-v-ac863f34=""
                    initial={{ opacity: 0, y: 4, scale: 0.98 }}
                    animate={{ opacity: 1, y: 0, scale: 1 }}
                    exit={{ opacity: 0, y: 2, scale: 0.98 }}
                    transition={SPRING_HEAVY_SNAP}
                  >
                    {detailNavigation.value ? (
                      <nav
                        aria-label={t("nav.nodeWorkspace") as any}
                        data-testid="node-detail-navigation"
                        className={__classes(
                          "workspace-nav node-detail-navigation",
                        )}
                        data-v-ac863f34=""
                      >
                        <button
                          type="button"
                          title={t("nav.returnHome") as any}
                          aria-label={t("nav.returnHome") as any}
                          className={__classes("node-navigation-back")}
                          onClick={(event: any) => {
                            navigate("nodes");
                          }}
                          data-v-ac863f34=""
                        >
                          <UiIcon name="back" data-v-ac863f34=""></UiIcon>
                          <span data-v-ac863f34="">{t("nav.returnHome")}</span>
                        </button>
                        <div
                          title={detailNavigation.value!.title as any}
                          className={__classes(
                            "workspace-label node-navigation-title",
                          )}
                          data-v-ac863f34=""
                        >
                          {detailNavigation.value!.title}
                        </div>
                        <Fragment>
                          {__each(detailNavigation.value!.sections, (item) => (
                            <button
                              key={item.id as any}
                              type="button"
                              title={item.label as any}
                              aria-label={item.label as any}
                              aria-current={
                                detailNavigation.value!.activeSection ===
                                item.id
                                  ? "page"
                                  : (undefined as any)
                              }
                              onClick={(event: any) => {
                                navigateNodeSection(item.id);
                              }}
                              data-v-ac863f34=""
                            >
                              <UiIcon
                                name={item.icon as any}
                                data-v-ac863f34=""
                              ></UiIcon>
                              <span data-v-ac863f34="">{item.label}</span>
                            </button>
                          ))}
                        </Fragment>
                      </nav>
                    ) : (
                      <Fragment>
                        <div
                          className={__classes("workspace-label")}
                          data-v-ac863f34=""
                        >
                          {t("nav.workspace")}
                        </div>
                        <nav
                          aria-label={t("nav.navigation") as any}
                          className={__classes("workspace-nav")}
                          data-v-ac863f34=""
                        >
                          <Fragment>
                            {__each(navigation, (item) => (
                              <button
                                key={item.screen as any}
                                type="button"
                                title={t(item.label) as any}
                                aria-label={t(item.label) as any}
                                aria-current={
                                  screen.value === item.screen
                                    ? "page"
                                    : (undefined as any)
                                }
                                onClick={(event: any) => {
                                  navigate(item.screen);
                                }}
                                data-v-ac863f34=""
                              >
                                <UiIcon
                                  name={item.icon as any}
                                  data-v-ac863f34=""
                                ></UiIcon>
                                <span data-v-ac863f34="">{t(item.label)}</span>
                              </button>
                            ))}
                          </Fragment>
                        </nav>
                      </Fragment>
                    )}
                  </motion.div>
                </AnimatePresence>
                <div
                  className={__classes("workspace-account")}
                  data-v-ac863f34=""
                >
                  <div
                    className={__classes("profile-avatar")}
                    data-v-ac863f34=""
                  >
                    {avatarSrc.value ? (
                      <img
                        src={avatarSrc.value as any}
                        alt=""
                        data-v-ac863f34=""
                      ></img>
                    ) : (
                      <span data-v-ac863f34="">
                        {visibleName.value.slice(0, 1)}
                      </span>
                    )}
                  </div>
                  <div
                    className={__classes("workspace-account-copy")}
                    data-v-ac863f34=""
                  >
                    <strong data-v-ac863f34="">{visibleName.value}</strong>
                    <span data-v-ac863f34="">{t("m.e19796712f1c")}</span>
                  </div>
                  <button
                    type="button"
                    disabled={busy.value as any}
                    aria-label={t("m.3ab8cc15939f") as any}
                    title={t("m.3ab8cc15939f") as any}
                    className={__classes("account-logout touch-target")}
                    onClick={(event: any) => {
                      __call(logout, event);
                    }}
                    data-v-ac863f34=""
                  >
                    <UiIcon name="logout" data-v-ac863f34=""></UiIcon>
                  </button>
                </div>
                {!mobileNavigation.value && !sidebarCollapsed.value ? (
                  <div
                    role="separator"
                    aria-orientation="vertical"
                    tabIndex={0}
                    aria-label={t("nav.resize") as any}
                    aria-valuemin={200 as any}
                    aria-valuemax={400 as any}
                    aria-valuenow={sidebarWidth.value as any}
                    className={__classes("sidebar-resize")}
                    onPointerDown={(event: any) => {
                      __call(resizeSidebar, event);
                    }}
                    onDoubleClick={resetSidebarWidth}
                    onKeyDown={(event: any) => {
                      __call(resizeSidebarKeys, event);
                    }}
                    data-v-ac863f34=""
                  ></div>
                ) : null}
              </aside>
            ) : null}
            <header className={__classes("topbar")} data-v-ac863f34="">
              {authenticated.value ? (
                <div
                  className={__classes("workspace-breadcrumb")}
                  data-v-ac863f34=""
                >
                  <button
                    ref={__dom(navigationToggle)}
                    type="button"
                    aria-label={
                      t(
                        mobileNavigation.value
                          ? "nav.navigation"
                          : sidebarCollapsed.value
                            ? "nav.expand"
                            : "nav.collapse",
                      ) as any
                    }
                    aria-expanded={
                      mobileNavigation.value
                        ? navigationOpen.value
                        : (!sidebarCollapsed.value as any)
                    }
                    aria-controls="workspace-navigation"
                    className={__classes("navigation-toggle touch-target")}
                    onClick={(event: any) => {
                      __call(toggleNavigation, event);
                    }}
                    data-v-ac863f34=""
                  >
                    <UiIcon
                      name={navigationOpen.value ? "close" : ("menu" as any)}
                      data-v-ac863f34=""
                    ></UiIcon>
                  </button>
                  <span
                    className={__classes("breadcrumb-root")}
                    data-v-ac863f34=""
                  >
                    {t("nav.workspace")}
                  </span>
                  <span
                    aria-hidden="true"
                    className={__classes("breadcrumb-divider")}
                    data-v-ac863f34=""
                  >
                    {"/"}
                  </span>
                  <strong data-v-ac863f34="">{screenTitle.value}</strong>
                </div>
              ) : (
                <a
                  href="/"
                  aria-label={t("m.f359facd4191") as any}
                  className={__classes("wordmark")}
                  data-v-ac863f34=""
                >
                  <span
                    aria-hidden="true"
                    className={__classes("brand-mark")}
                    data-v-ac863f34=""
                  >
                    <i data-v-ac863f34=""></i>
                    <i data-v-ac863f34=""></i>
                    <i data-v-ac863f34=""></i>
                  </span>
                  <span data-v-ac863f34="">{"NodeDance"}</span>
                </a>
              )}
              <div className={__classes("topbar-right")} data-v-ac863f34="">
                <UiSelect
                  key={viewHash(activeRoute.value) as any}
                  menuClass="language-options"
                  aria-label={
                    locale.value === "en" ? "Language" : ("语言" as any)
                  }
                  value={locale.value as any}
                  className={__classes("language-selector")}
                  onChange={(event: any) => {
                    setLocale(
                      (event.target as HTMLSelectElement).value as Locale,
                    );
                  }}
                  data-v-ac863f34=""
                >
                  <option value="zh-CN" data-v-ac863f34="">
                    {"简体中文"}
                  </option>
                  <option value="en" data-v-ac863f34="">
                    {"English"}
                  </option>
                </UiSelect>
              </div>
            </header>
            {authenticated.value && initialCredentials.value ? (
              <section
                aria-labelledby="initial-credentials-title"
                className={__classes("initial-credentials-card")}
                data-v-ac863f34=""
              >
                <h2 id="initial-credentials-title" data-v-ac863f34="">
                  {t("auth.generatedTitle")}
                </h2>
                <p data-v-ac863f34="">{t("auth.generatedHint")}</p>
                <dl data-v-ac863f34="">
                  <div data-v-ac863f34="">
                    <dt data-v-ac863f34="">{t("auth.username")}</dt>
                    <dd data-v-ac863f34="">
                      <code data-v-ac863f34="">
                        {initialCredentials.value.username}
                      </code>
                    </dd>
                  </div>
                  {initialCredentials.value.password ? (
                    <div data-v-ac863f34="">
                      <dt data-v-ac863f34="">{t("m.c19091521d71")}</dt>
                      <dd data-v-ac863f34="">
                        <code data-v-ac863f34="">
                          {initialCredentials.value.password}
                        </code>
                      </dd>
                    </div>
                  ) : null}
                </dl>
                <button
                  type="button"
                  className={__classes("secondary-button")}
                  onClick={(event: any) => {
                    initialCredentials.value = null;
                  }}
                  data-v-ac863f34=""
                >
                  {t("auth.saved")}
                </button>
              </section>
            ) : null}
            {authenticated.value &&
            screen.value !== "settings" &&
            notice.value ? (
              <div
                role="status"
                className={__classes("alert alert-success workspace-notice")}
                data-v-ac863f34=""
              >
                {display(notice.value)}
              </div>
            ) : null}
            {authenticated.value &&
            screen.value !== "settings" &&
            error.value ? (
              <div
                role="alert"
                className={__classes("alert alert-error workspace-notice")}
                data-v-ac863f34=""
              >
                {display(error.value)}
              </div>
            ) : null}
            {screen.value === "loading" ? (
              <div
                role="status"
                aria-live="polite"
                className={__classes("center-stage")}
                data-v-ac863f34=""
              >
                <div className={__classes("loader")} data-v-ac863f34=""></div>
                <p data-v-ac863f34="">{t("m.a62a3d2a07c9")}</p>
              </div>
            ) : screen.value === "unavailable" ? (
              <section className={__classes("center-stage")} data-v-ac863f34="">
                <div
                  className={__classes("standalone-card")}
                  data-v-ac863f34=""
                >
                  <span className={__classes("eyebrow")} data-v-ac863f34="">
                    {t("label.638731745e22")}
                  </span>
                  <h1 data-v-ac863f34="">{t("m.c579038c5ec3")}</h1>
                  <p data-v-ac863f34="">{t("m.3ee483b99392")}</p>
                  {error.value ? (
                    <div
                      role="alert"
                      className={__classes("alert alert-error")}
                      data-v-ac863f34=""
                    >
                      {display(error.value)}
                    </div>
                  ) : null}
                  <button
                    type="button"
                    className={__classes("primary-button")}
                    onClick={(event: any) => {
                      __call(initializeScreen, event);
                    }}
                    data-v-ac863f34=""
                  >
                    {t("m.68891ab35cab")}
                    <span aria-hidden="true" data-v-ac863f34="">
                      {"↗"}
                    </span>
                  </button>
                </div>
              </section>
            ) : screen.value === "setup" ? (
              <section className={__classes("auth-layout")} data-v-ac863f34="">
                <div className={__classes("auth-copy")} data-v-ac863f34="">
                  <span className={__classes("eyebrow")} data-v-ac863f34="">
                    {t("label.fec911d2155d")}
                  </span>
                  <h1 data-v-ac863f34="">
                    {t("m.b640c195b999")}
                    <br data-v-ac863f34=""></br>
                    <em data-v-ac863f34="">{t("m.c89b43673182")}</em>
                  </h1>
                  <p data-v-ac863f34="">{t("m.5abf65737f4c")}</p>
                  <div className={__classes("feature-row")} data-v-ac863f34="">
                    <span
                      className={__classes("feature-symbol")}
                      data-v-ac863f34=""
                    >
                      {"✳"}
                    </span>
                    <span data-v-ac863f34="">{t("m.caa90381315a")}</span>
                  </div>
                </div>
                <section
                  aria-labelledby="form-title"
                  className={__classes("form-card")}
                  data-v-ac863f34=""
                >
                  <div className={__classes("card-heading")} data-v-ac863f34="">
                    <span className={__classes("eyebrow")} data-v-ac863f34="">
                      {t("label.04b7b6c5d04d")}
                    </span>
                    <span
                      className={__classes("step-indicator")}
                      data-v-ac863f34=""
                    >
                      <span
                        className={__classes("step-current")}
                        data-v-ac863f34=""
                      >
                        {"01"}
                      </span>
                      <span
                        className={__classes("step-line")}
                        data-v-ac863f34=""
                      ></span>
                      {"02"}
                    </span>
                  </div>
                  <h2 id="form-title" data-v-ac863f34="">
                    {t("m.bcea39d8590e")}
                  </h2>
                  <p className={__classes("card-intro")} data-v-ac863f34="">
                    {t("m.07c18f374537")}
                  </p>
                  {error.value ? (
                    <div
                      role="alert"
                      className={__classes("alert alert-error")}
                      data-v-ac863f34=""
                    >
                      {display(error.value)}
                    </div>
                  ) : null}
                  <form
                    className={__classes("form-stack")}
                    onSubmit={(event: any) => {
                      event.preventDefault();
                      __call(submitSetup, event);
                    }}
                    data-v-ac863f34=""
                  >
                    <label className={__classes("field")} data-v-ac863f34="">
                      <span data-v-ac863f34="">{t("m.fc482bd2d5bf")}</span>
                      <input
                        name="credential"
                        type="password"
                        autoComplete="one-time-code"
                        required={true}
                        placeholder={t("m.f1f7cbf5f906") as any}
                        {...__input(
                          setupForm.credential,
                          (__nextValue: any) => {
                            setupForm.credential = __nextValue;
                          },
                          "password",
                          undefined,
                          [],
                        )}
                        data-v-ac863f34=""
                      ></input>
                      <small
                        className={__classes("field-hint")}
                        data-v-ac863f34=""
                      >
                        {t("m.59b4ebe09d0f")}
                      </small>
                    </label>
                    <label className={__classes("field")} data-v-ac863f34="">
                      <span data-v-ac863f34="">{t("auth.username")}</span>
                      <input
                        name="username"
                        autoComplete="username"
                        maxLength={80}
                        placeholder={t("auth.generateUsername") as any}
                        {...__input(
                          setupForm.username,
                          (__nextValue: any) => {
                            setupForm.username = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-ac863f34=""
                      ></input>
                      <small
                        className={__classes("field-hint")}
                        data-v-ac863f34=""
                      >
                        {t("auth.usernameHint")}
                      </small>
                    </label>
                    <label className={__classes("field")} data-v-ac863f34="">
                      <span data-v-ac863f34="">{t("m.c19091521d71")}</span>
                      <input
                        name="password"
                        type="password"
                        autoComplete="new-password"
                        placeholder={t("auth.generatePassword") as any}
                        {...__input(
                          setupForm.password,
                          (__nextValue: any) => {
                            setupForm.password = __nextValue;
                          },
                          "password",
                          undefined,
                          [],
                        )}
                        data-v-ac863f34=""
                      ></input>
                      <small
                        className={__classes("field-hint")}
                        data-v-ac863f34=""
                      >
                        {t("m.eb0831f61840")}
                      </small>
                    </label>
                    <button
                      type="submit"
                      disabled={busy.value as any}
                      className={__classes("primary-button full-button")}
                      data-v-ac863f34=""
                    >
                      {busy.value ? busyLabel.value : t("m.b95a201cc95d")}{" "}
                      <span aria-hidden="true" data-v-ac863f34="">
                        {"↗"}
                      </span>
                    </button>
                  </form>
                  <p className={__classes("privacy-note")} data-v-ac863f34="">
                    <span aria-hidden="true" data-v-ac863f34="">
                      {"◈"}
                    </span>
                    {t("m.1e6ae53da093")}
                  </p>
                </section>
              </section>
            ) : screen.value === "login" ? (
              <section
                className={__classes("auth-layout login-layout")}
                data-v-ac863f34=""
              >
                <div
                  className={__classes("auth-copy login-copy")}
                  data-v-ac863f34=""
                >
                  <span className={__classes("eyebrow")} data-v-ac863f34="">
                    {t("label.111bdc0c1ef5")}
                  </span>
                  <h1 data-v-ac863f34="">{t("m.cb1d97794707")}</h1>
                  <p data-v-ac863f34="">{t("m.cd2de59b2219")}</p>
                  <div
                    className={__classes("login-identity")}
                    data-v-ac863f34=""
                  >
                    <div
                      className={__classes("preview-avatar")}
                      data-v-ac863f34=""
                    >
                      {avatarSrc.value ? (
                        <img
                          key={avatarRevision.value as any}
                          src={avatarSrc.value as any}
                          alt=""
                          data-v-ac863f34=""
                        ></img>
                      ) : (
                        <span aria-hidden="true" data-v-ac863f34="">
                          {(appearance.displayName || "N").slice(0, 1)}
                        </span>
                      )}
                    </div>
                    <div data-v-ac863f34="">
                      <span
                        className={__classes("preview-label")}
                        data-v-ac863f34=""
                      >
                        {t("label.e00e2736dd16")}
                      </span>
                      <strong data-v-ac863f34="">
                        {appearance.displayName || "NodeDance"}
                      </strong>
                    </div>
                  </div>
                </div>
                <section
                  aria-labelledby="login-title"
                  className={__classes("form-card login-card")}
                  data-v-ac863f34=""
                >
                  <span className={__classes("eyebrow")} data-v-ac863f34="">
                    {t("label.fe274f9f3a26")}
                  </span>
                  <h2 id="login-title" data-v-ac863f34="">
                    {t("m.c454c61e78ab")}
                  </h2>
                  <p className={__classes("card-intro")} data-v-ac863f34="">
                    {t("m.d62d9f683b33")}
                  </p>
                  {notice.value ? (
                    <div
                      role="status"
                      className={__classes("alert alert-success")}
                      data-v-ac863f34=""
                    >
                      {display(notice.value)}
                    </div>
                  ) : null}
                  {error.value ? (
                    <div
                      role="alert"
                      className={__classes("alert alert-error")}
                      data-v-ac863f34=""
                    >
                      {display(error.value)}
                    </div>
                  ) : null}
                  <form
                    className={__classes("form-stack")}
                    onSubmit={(event: any) => {
                      event.preventDefault();
                      __call(submitLogin, event);
                    }}
                    data-v-ac863f34=""
                  >
                    <label className={__classes("field")} data-v-ac863f34="">
                      <span data-v-ac863f34="">{t("auth.username")}</span>
                      <input
                        name="username"
                        autoComplete="username"
                        required={true}
                        autoFocus={true}
                        maxLength={80}
                        placeholder={t("auth.username") as any}
                        {...__input(
                          loginUsername.value,
                          (__nextValue: any) => {
                            loginUsername.value = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-ac863f34=""
                      ></input>
                    </label>
                    <label className={__classes("field")} data-v-ac863f34="">
                      <span data-v-ac863f34="">{t("m.c19091521d71")}</span>
                      <input
                        name="password"
                        type="password"
                        autoComplete="current-password"
                        required={true}
                        placeholder={t("m.7673702e6ff6") as any}
                        {...__input(
                          loginPassword.value,
                          (__nextValue: any) => {
                            loginPassword.value = __nextValue;
                          },
                          "password",
                          undefined,
                          [],
                        )}
                        data-v-ac863f34=""
                      ></input>
                    </label>
                    <button
                      type="submit"
                      disabled={busy.value as any}
                      className={__classes("primary-button full-button")}
                      data-v-ac863f34=""
                    >
                      {busy.value ? busyLabel.value : t("m.d19038809234")}{" "}
                      <span aria-hidden="true" data-v-ac863f34="">
                        {"↗"}
                      </span>
                    </button>
                  </form>
                  {guestEnabled.value ? (
                    <button
                      type="button"
                      disabled={busy.value as any}
                      className={__classes(
                        "secondary-button full-button guest-entry",
                      )}
                      onClick={(event: any) => {
                        navigate("guest");
                      }}
                      data-v-ac863f34=""
                    >
                      <UiIcon name="guest" data-v-ac863f34=""></UiIcon>
                      {t("guest.enter")}
                    </button>
                  ) : null}
                  <p className={__classes("privacy-note")} data-v-ac863f34="">
                    <span aria-hidden="true" data-v-ac863f34="">
                      {"◈"}
                    </span>
                    {t("m.3834cf7462ca")}
                  </p>
                </section>
              </section>
            ) : screen.value === "guest" ? (
              <GuestDisplay
                onBack={(event: any) => {
                  navigate("nodes");
                }}
                data-v-ac863f34=""
              ></GuestDisplay>
            ) : screen.value === "guest-settings" ? (
              <GuestSettings data-v-ac863f34=""></GuestSettings>
            ) : screen.value === "nodes" ? (
              <NodesDashboard
                onEnroll={(event: any) => {
                  navigate("agent-enrollment");
                }}
                data-v-ac863f34=""
              ></NodesDashboard>
            ) : screen.value === "agent-enrollment" ? (
              <AgentEnrollment
                onBack={(event: any) => {
                  navigate("nodes");
                }}
                data-v-ac863f34=""
              ></AgentEnrollment>
            ) : screen.value === "host-management" ? (
              <HostManagement data-v-ac863f34=""></HostManagement>
            ) : screen.value === "updates" ? (
              <UpdateCenter data-v-ac863f34=""></UpdateCenter>
            ) : screen.value === "feeds" ? (
              <FeedsCenter data-v-ac863f34=""></FeedsCenter>
            ) : screen.value === "operations" ? (
              <OperationConsole data-v-ac863f34=""></OperationConsole>
            ) : screen.value === "alerts" ? (
              <AlertCenter data-v-ac863f34=""></AlertCenter>
            ) : screen.value === "discovery" ? (
              <TailscaleDiscovery
                onBack={(event: any) => {
                  navigate("nodes");
                }}
                data-v-ac863f34=""
              ></TailscaleDiscovery>
            ) : (
              <section
                className={__classes("settings-layout")}
                data-v-ac863f34=""
              >
                <aside
                  className={__classes("settings-sidebar")}
                  data-v-ac863f34=""
                >
                  <h1 data-v-ac863f34="">{t("m.df3d58c7d84b")}</h1>
                  <div
                    className={__classes("sidebar-profile")}
                    data-v-ac863f34=""
                  >
                    <div
                      className={__classes("profile-avatar")}
                      data-v-ac863f34=""
                    >
                      {avatarSrc.value ? (
                        <img
                          key={avatarRevision.value as any}
                          src={avatarSrc.value as any}
                          alt=""
                          data-v-ac863f34=""
                        ></img>
                      ) : (
                        <span aria-hidden="true" data-v-ac863f34="">
                          {visibleName.value.slice(0, 1)}
                        </span>
                      )}
                    </div>
                    <div
                      className={__classes("profile-summary")}
                      data-v-ac863f34=""
                    >
                      <strong data-v-ac863f34="">{visibleName.value}</strong>
                      <span data-v-ac863f34="">{t("m.e19796712f1c")}</span>
                    </div>
                    <span
                      aria-hidden="true"
                      className={__classes("profile-dot")}
                      data-v-ac863f34=""
                    ></span>
                  </div>
                </aside>
                <div className={__classes("settings-main")} data-v-ac863f34="">
                  <div
                    className={__classes("settings-heading")}
                    data-v-ac863f34=""
                  >
                    <div data-v-ac863f34="">
                      <span className={__classes("eyebrow")} data-v-ac863f34="">
                        {t("label.812ff062d7b7")}
                      </span>
                      <h2 data-v-ac863f34="">{t("m.0bb1a9d13722")}</h2>
                    </div>
                  </div>
                  {notice.value ? (
                    <div
                      role="status"
                      className={__classes("alert alert-success page-alert")}
                      data-v-ac863f34=""
                    >
                      {display(notice.value)}
                    </div>
                  ) : null}
                  {error.value ? (
                    <div
                      role="alert"
                      className={__classes("alert alert-error page-alert")}
                      data-v-ac863f34=""
                    >
                      {display(error.value)}
                    </div>
                  ) : null}
                  <section
                    aria-labelledby="appearance-heading"
                    className={__classes("settings-card appearance-card")}
                    data-v-ac863f34=""
                  >
                    <div
                      className={__classes("section-heading")}
                      data-v-ac863f34=""
                    >
                      <div
                        aria-hidden="true"
                        className={__classes("section-icon appearance-icon")}
                        data-v-ac863f34=""
                      >
                        {"◉"}
                      </div>
                      <div data-v-ac863f34="">
                        <h3 id="appearance-heading" data-v-ac863f34="">
                          {t("m.ad429c44450e")}
                        </h3>
                        <p data-v-ac863f34="">{t("m.d6fb19acbade")}</p>
                      </div>
                    </div>
                    <form
                      className={__classes("profile-form")}
                      onSubmit={(event: any) => {
                        event.preventDefault();
                        __call(saveProfile, event);
                      }}
                      data-v-ac863f34=""
                    >
                      <div
                        className={__classes("profile-edit-row")}
                        data-v-ac863f34=""
                      >
                        <div
                          className={__classes("profile-avatar large-avatar")}
                          data-v-ac863f34=""
                        >
                          {avatarSrc.value ? (
                            <img
                              key={avatarRevision.value as any}
                              src={avatarSrc.value as any}
                              alt=""
                              data-v-ac863f34=""
                            ></img>
                          ) : (
                            <span aria-hidden="true" data-v-ac863f34="">
                              {visibleName.value.slice(0, 1)}
                            </span>
                          )}
                        </div>
                        <div
                          className={__classes("upload-copy")}
                          data-v-ac863f34=""
                        >
                          <strong data-v-ac863f34="">
                            {t("m.3ea2d23c902c")}
                          </strong>
                          <span data-v-ac863f34="">{t("m.b8ed7facdbe2")}</span>
                        </div>
                        <label
                          className={__classes(
                            "secondary-button upload-button",
                          )}
                          data-v-ac863f34=""
                        >
                          {uploading.value === "avatar"
                            ? t("m.ce1c9d61565f")
                            : t("m.42ece898483f")}{" "}
                          <input
                            aria-label={t("m.d7d2344d545d") as any}
                            type="file"
                            accept="image/png,image/jpeg,image/gif"
                            disabled={!!uploading.value as any}
                            onChange={(event: any) => {
                              handleImageChange("avatar", event);
                            }}
                            data-v-ac863f34=""
                          ></input>
                        </label>
                      </div>
                      <label
                        className={__classes("field profile-name-field")}
                        data-v-ac863f34=""
                      >
                        <span data-v-ac863f34="">{t("m.1264cd049636")}</span>
                        <input
                          name="profileDisplayName"
                          autoComplete="nickname"
                          required={true}
                          maxLength={80}
                          {...__input(
                            appearance.displayName,
                            (__nextValue: any) => {
                              appearance.displayName = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-ac863f34=""
                        ></input>
                        <small
                          className={__classes("field-hint")}
                          data-v-ac863f34=""
                        >
                          {t("m.f673747ede6e")}
                        </small>
                      </label>
                      <div className={__classes("field")} data-v-ac863f34="">
                        <span data-v-ac863f34="">{t("m.8f29293906e7")}</span>
                        <div
                          role="group"
                          aria-label={t("m.8f29293906e7") as any}
                          className={__classes("theme-options")}
                          data-v-ac863f34=""
                        >
                          <button
                            type="button"
                            aria-pressed={appearance.theme === ("dark" as any)}
                            className={__classes("theme-option", {
                              selected: appearance.theme === "dark",
                            })}
                            onClick={(event: any) => {
                              appearance.theme = "dark";
                            }}
                            data-v-ac863f34=""
                          >
                            <span
                              className={__classes("theme-swatch dark-swatch")}
                              data-v-ac863f34=""
                            >
                              <i data-v-ac863f34=""></i>
                              <i data-v-ac863f34=""></i>
                              <i data-v-ac863f34=""></i>
                            </span>
                            <span data-v-ac863f34="">
                              {t("m.a6b75d068032")}
                            </span>
                          </button>
                          <button
                            type="button"
                            aria-pressed={
                              appearance.theme === ("system" as any)
                            }
                            className={__classes("theme-option", {
                              selected: appearance.theme === "system",
                            })}
                            onClick={(event: any) => {
                              appearance.theme = "system";
                            }}
                            data-v-ac863f34=""
                          >
                            <span
                              className={__classes(
                                "theme-swatch system-swatch",
                              )}
                              data-v-ac863f34=""
                            >
                              <i data-v-ac863f34=""></i>
                              <i data-v-ac863f34=""></i>
                              <i data-v-ac863f34=""></i>
                            </span>
                            <span data-v-ac863f34="">
                              {t("m.217cfe7db1e3")}
                            </span>
                          </button>
                          <button
                            type="button"
                            aria-pressed={appearance.theme === ("light" as any)}
                            className={__classes("theme-option", {
                              selected: appearance.theme === "light",
                            })}
                            onClick={(event: any) => {
                              appearance.theme = "light";
                            }}
                            data-v-ac863f34=""
                          >
                            <span
                              className={__classes("theme-swatch light-swatch")}
                              data-v-ac863f34=""
                            >
                              <i data-v-ac863f34=""></i>
                              <i data-v-ac863f34=""></i>
                              <i data-v-ac863f34=""></i>
                            </span>
                            <span data-v-ac863f34="">
                              {t("m.aa0819dfc4d8")}
                            </span>
                          </button>
                        </div>
                      </div>
                      <div
                        className={__classes("field color-field")}
                        data-v-ac863f34=""
                      >
                        <span data-v-ac863f34="">{t("m.882fe2e84241")}</span>
                        <div
                          className={__classes("color-control")}
                          data-v-ac863f34=""
                        >
                          <input
                            aria-label={t("m.882fe2e84241") as any}
                            type="color"
                            {...__input(
                              appearance.backgroundColor,
                              (__nextValue: any) => {
                                appearance.backgroundColor = __nextValue;
                              },
                              "color",
                              undefined,
                              [],
                            )}
                            data-v-ac863f34=""
                          ></input>
                          <code data-v-ac863f34="">
                            {safeBackgroundColor.value.toUpperCase()}
                          </code>
                        </div>
                      </div>
                      <div
                        className={__classes("background-upload-row")}
                        data-v-ac863f34=""
                      >
                        <div
                          className={__classes("background-preview")}
                          style={__style({
                            backgroundColor: safeBackgroundColor.value,
                          })}
                          data-v-ac863f34=""
                        >
                          {backgroundSrc.value ? (
                            <img
                              key={backgroundRevision.value as any}
                              src={backgroundSrc.value as any}
                              alt={t("m.ac485672fc03") as any}
                              data-v-ac863f34=""
                            ></img>
                          ) : (
                            <span data-v-ac863f34="">
                              {t("label.c44379727315")}
                            </span>
                          )}
                        </div>
                        <div
                          className={__classes("upload-copy")}
                          data-v-ac863f34=""
                        >
                          <strong data-v-ac863f34="">
                            {t("m.99cf84163dfc")}
                          </strong>
                          <span data-v-ac863f34="">{t("m.b07e86c480d1")}</span>
                        </div>
                        <label
                          className={__classes(
                            "secondary-button upload-button",
                          )}
                          data-v-ac863f34=""
                        >
                          {uploading.value === "background"
                            ? t("m.ce1c9d61565f")
                            : t("m.9fbb4750bb39")}{" "}
                          <input
                            aria-label={t("m.276341ecaf37") as any}
                            type="file"
                            accept="image/png,image/jpeg,image/gif"
                            disabled={!!uploading.value as any}
                            onChange={(event: any) => {
                              handleImageChange("background", event);
                            }}
                            data-v-ac863f34=""
                          ></input>
                        </label>
                      </div>
                      <div
                        className={__classes("form-actions")}
                        data-v-ac863f34=""
                      >
                        <button
                          type="submit"
                          disabled={busy.value as any}
                          className={__classes("primary-button")}
                          data-v-ac863f34=""
                        >
                          {busy.value ? busyLabel.value : t("m.200fec26bd0e")}{" "}
                          <span aria-hidden="true" data-v-ac863f34="">
                            {"↗"}
                          </span>
                        </button>
                      </div>
                    </form>
                  </section>
                  <section
                    aria-labelledby="security-heading"
                    className={__classes("settings-card security-card")}
                    data-v-ac863f34=""
                  >
                    <div
                      className={__classes("section-heading")}
                      data-v-ac863f34=""
                    >
                      <div
                        aria-hidden="true"
                        className={__classes("section-icon security-icon")}
                        data-v-ac863f34=""
                      >
                        {"⌑"}
                      </div>
                      <div data-v-ac863f34="">
                        <h3 id="security-heading" data-v-ac863f34="">
                          {t("m.1b5365b35720")}
                        </h3>
                        <p data-v-ac863f34="">{t("m.910e765e0b31")}</p>
                      </div>
                    </div>
                    <form
                      className={__classes("password-form")}
                      onSubmit={(event: any) => {
                        event.preventDefault();
                        __call(changePassword, event);
                      }}
                      data-v-ac863f34=""
                    >
                      <label className={__classes("field")} data-v-ac863f34="">
                        <span data-v-ac863f34="">{t("m.a114cfb687e6")}</span>
                        <input
                          type="password"
                          autoComplete="current-password"
                          required={true}
                          {...__input(
                            passwordForm.currentPassword,
                            (__nextValue: any) => {
                              passwordForm.currentPassword = __nextValue;
                            },
                            "password",
                            undefined,
                            [],
                          )}
                          data-v-ac863f34=""
                        ></input>
                      </label>
                      <div
                        className={__classes("password-pair")}
                        data-v-ac863f34=""
                      >
                        <label
                          className={__classes("field")}
                          data-v-ac863f34=""
                        >
                          <span data-v-ac863f34="">{t("m.515e9c7cf7b2")}</span>
                          <input
                            type="password"
                            autoComplete="new-password"
                            required={true}
                            {...__input(
                              passwordForm.newPassword,
                              (__nextValue: any) => {
                                passwordForm.newPassword = __nextValue;
                              },
                              "password",
                              undefined,
                              [],
                            )}
                            data-v-ac863f34=""
                          ></input>
                        </label>
                        <label
                          className={__classes("field")}
                          data-v-ac863f34=""
                        >
                          <span data-v-ac863f34="">{t("m.6fde05a916c6")}</span>
                          <input
                            type="password"
                            autoComplete="new-password"
                            required={true}
                            {...__input(
                              passwordForm.confirmPassword,
                              (__nextValue: any) => {
                                passwordForm.confirmPassword = __nextValue;
                              },
                              "password",
                              undefined,
                              [],
                            )}
                            data-v-ac863f34=""
                          ></input>
                        </label>
                      </div>
                      <small
                        className={__classes("field-hint")}
                        data-v-ac863f34=""
                      >
                        {t("m.2d990ebc16e1")}
                      </small>
                      <div
                        className={__classes("form-actions")}
                        data-v-ac863f34=""
                      >
                        <button
                          type="submit"
                          disabled={busy.value as any}
                          className={__classes("secondary-button")}
                          data-v-ac863f34=""
                        >
                          {t("m.828170843089")}
                        </button>
                      </div>
                    </form>
                  </section>
                  <section
                    aria-labelledby="sessions-heading"
                    className={__classes("settings-card sessions-card")}
                    data-v-ac863f34=""
                  >
                    <div
                      className={__classes("section-heading session-heading")}
                      data-v-ac863f34=""
                    >
                      <div
                        aria-hidden="true"
                        className={__classes("section-icon session-icon")}
                        data-v-ac863f34=""
                      >
                        {"⌘"}
                      </div>
                      <div data-v-ac863f34="">
                        <h3 id="sessions-heading" data-v-ac863f34="">
                          {t("m.9fd94953b94b")}
                        </h3>
                        <p data-v-ac863f34="">{t("m.986f34b8224e")}</p>
                      </div>
                      <span
                        className={__classes("session-count")}
                        data-v-ac863f34=""
                      >
                        {sessions.value.length}
                        {t("m.3a116815bc9d")}
                      </span>
                    </div>
                    {sessions.value.length ? (
                      <div
                        className={__classes("session-list")}
                        data-v-ac863f34=""
                      >
                        <Fragment>
                          {__each(sessions.value, (session) => (
                            <article
                              key={session.id as any}
                              className={__classes("session-item")}
                              data-v-ac863f34=""
                            >
                              <div
                                aria-hidden="true"
                                className={__classes("device-icon")}
                                data-v-ac863f34=""
                              >
                                {"▣"}
                              </div>
                              <div
                                className={__classes("session-info")}
                                data-v-ac863f34=""
                              >
                                <strong data-v-ac863f34="">
                                  {session.device || t("m.8ca01a9ba438")}
                                  {session.current ? (
                                    <span
                                      className={__classes("current-badge")}
                                      data-v-ac863f34=""
                                    >
                                      {t("m.c68978537cf3")}
                                    </span>
                                  ) : null}
                                </strong>
                                <span data-v-ac863f34="">
                                  {session.remoteAddr || t("m.f4a131451fce")}
                                </span>
                                <small data-v-ac863f34="">
                                  {t("m.41f5cada11ed")}
                                  {formatTime(session.lastSeenAt)}
                                </small>
                              </div>
                              <button
                                type="button"
                                disabled={
                                  busy.value || (session.current as any)
                                }
                                aria-label={
                                  session.current
                                    ? t("m.62a9388a42a8")
                                    : (t("m.6cfb5f2a5be8", {
                                        "0":
                                          session.device || t("m.8ca01a9ba438"),
                                      }) as any)
                                }
                                className={__classes("revoke-button")}
                                onClick={(event: any) => {
                                  revokeSession(session);
                                }}
                                data-v-ac863f34=""
                              >
                                {session.current
                                  ? t("m.cb62ebd689ee")
                                  : t("m.926a50b98ece")}
                              </button>
                            </article>
                          ))}
                        </Fragment>
                      </div>
                    ) : (
                      <p
                        className={__classes("empty-sessions")}
                        data-v-ac863f34=""
                      >
                        {t("m.214dc45ab589")}
                      </p>
                    )}
                  </section>
                </div>
              </section>
            )}
            <UiDialog data-v-ac863f34=""></UiDialog>
          </main>
        </>
      );
    },
    input,
    [],
    [],
    {},
    true,
  );
}
