// Isolated browser fixtures; discovers the operation catalog from a running Core.
// No fixture request reaches the connected Core.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const out = path.resolve(__dirname, "../../.build/web-design-audit");
const webURL = process.env.NODEDANCE_WEB_AUDIT_URL || "http://127.0.0.1:5190";
const coreURL =
  process.env.NODEDANCE_CORE_AUDIT_URL || "http://127.0.0.1:18192";
const snapshots = [];
const enMessages = require("../src/locales/en.json"), zhMessages = require("../src/locales/zh-CN.json");
fs.mkdirSync(out, { recursive: true });
(async () => {
  const catalogResponse = await fetch(coreURL + "/api/v1/operations", {
    signal: AbortSignal.timeout(15000),
  });
  assert.equal(catalogResponse.status, 200);
  const catalog = await catalogResponse.json();
  assert.equal(catalog.version, 1);
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1050 },
    });
    let theme = "light",
      signedIn = true,
      initialized = true,
      populated = false,
      unavailable = false,
      discoveryAvailable = false;
    const terminalOnly = process.env.NODEDANCE_WEB_TERMINAL_ONLY === "1";
    const terminalID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    let terminalCreates = 0, terminalResumes = 0, terminalEnds = 0;
    let terminalSocketCount = 0;
    const resumeBodies = [];
    page.setDefaultTimeout(8000);
    const errors = [];
    page.on("pageerror", (e) => {
      errors.push(e.message);
      console.log("PAGE ERROR:", e.stack);
    });
    const nodes = [
      {
        nodeId: "node-production",
        agentId: "agent-production",
        displayName: "Production · Tokyo",
        status: "online",
        generation: 1,
        capabilities: ["docker", "files", "terminal", "agent.update.v1"],
        agentVersion: "0.1.0",
      },
      {
        nodeId: "node-edge",
        agentId: "agent-edge",
        displayName: "Edge · Singapore",
        status: "offline",
        generation: 1,
        capabilities: [],
        agentVersion: "0.1.0",
      },
      {
        nodeId: "node-new",
        displayName: "Staging · Frankfurt",
        status: "pending",
        generation: 0,
        capabilities: [],
      },
    ];
    const settings = {
      viewMode: "monitor",
      groupBy: "node",
      sortBy: "custom",
      nodeGroupBy: "status",
      nodeSortBy: "custom",
      featuredLimit: 4,
      customFields: ["state", "ports", "health"],
    };
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url()),
        p = url.pathname,
        now = new Date().toISOString();
      let body = {};
      let status = 200;
      if (p === "/api/v1/operations") body = catalog;
      else if (p.endsWith("/agent-settings")) body={settings:{disableRemoteControl:false,memoryMode:"available",customIpv4:"",customIpv6:"",fastIntervalSeconds:5,diskIntervalSeconds:30,includeInterfaces:[],excludeInterfaces:[],includeMountpoints:[],billingDay:1,enableGpu:true,dns:"",preferIpVersion:"",compression:true,transport:"auto",publicIpUrl:"",autoUpdate:false,updateIntervalHours:6}};
      else if (p.endsWith("/asset")) body={price:9,currency:"USD",billingCycleDays:30,expiryNoticeDays:7,trafficLimitBytes:1099511627776,trafficAccounting:"sum",trafficWarningPercent:90};
      else if (p.endsWith("/commands/batches")) body={batches:[]};
      else if (p.endsWith("/agents/enrollment-grants")) body={grants:[]};
      else if (p.endsWith("/metrics/catalog")) body={metrics:{"swap.used_percent":"percent","load.one":"load","gpu.temperature_celsius":"celsius"}};
      else if (p.endsWith("/agents/enrollments"))
        body = {
          token: "isolated-audit-token",
          displayName: "Audit node",
          expiresAt: now,
          nativeInstallCommand:
            "curl -fsSL https://raw.githubusercontent.com/CST-Cat/NodeDance/main/scripts/install-agent.sh | sudo bash -s -- --mode native --server https://core.example --token isolated-audit-token",
          dockerInstallCommand:
            "curl -fsSL https://raw.githubusercontent.com/CST-Cat/NodeDance/main/scripts/install-agent.sh | sudo bash -s -- --mode docker --server https://core.example --token isolated-audit-token",
        };
      else if (terminalOnly && p.includes("/terminal")) {
        if (route.request().method() === "DELETE") { terminalEnds++; status = 204; }
        else if (p.endsWith("/resume")) {
          terminalResumes++; resumeBodies.push(route.request().postDataJSON());
          if (terminalResumes === 1) { status = 503; body = {message:"temporary outage"}; }
          else body = { streamId: terminalID, ticket: "resumed-ticket", expiresAt: now };
        } else {terminalCreates++; body = {streamId:terminalID,ticket:"initial-ticket",expiresAt:now};}
      }
      else if (p.includes("/terminal")) {
        status = 503;
        body = { error: "Terminal unavailable in isolated design audit" };
      } else if (p.endsWith("/discovery/deployments")) body = { tasks: [] };
      else if (p.includes("/updates/check"))
        body = {
          component: "core",
          current: "core-v0.1.8",
          latest: "core-v0.1.9",
          available: true,
          configured: true,
          compatible: true,
          mode: "docker",
          release: {
            url: "https://github.com/CST-Cat/NodeDance/releases/tag/core-v0.1.9",
            notes: "Design audit fixture",
          },
        };
      else if (p.includes("/updates/status")) body = { jobs: [] };
      else if (p.endsWith("/rebuild/plan"))
        body = {
          containerId: "container-nginx",
          name: "nginx-production",
          imageId: "sha256:test",
          wasRunning: true,
          writableLayerBytes: 0,
          snapshotRequired: false,
          portsBefore: [],
          portsAfter: [],
          preserved: ["Volumes and networks"],
          changed: ["Container identity"],
          downtime: "A short restart",
          risks: ["Review before applying"],
          mounts: [],
        };
      else if (p.endsWith("/images"))
        body = {
          images: [
            {
              id: "sha256:test",
              tags: ["nginx:stable"],
              digests: [],
              size: 12345678,
              createdAt: 1710000000,
              containers: 1,
            },
          ],
          total: 1,
          page: 0,
        };
      else if (p.includes("/compose/projects/") && p.includes("/config/"))
        body = {
          fileIndex: 0,
          content: "services:\n  app:\n    image: nginx:stable",
          sha256: "test",
        };
      else if (p.includes("/compose/projects"))
        body = {
          projects: [
            {
              ref: {
                key: "web-stack",
                name: "Web stack",
                workingDirectory: "/srv/web-stack",
                configFiles: ["/srv/web-stack/compose.yaml"],
              },
              configAvailable: true,
              services: [
                {
                  name: "app",
                  instances: [
                    {
                      containerId: "container-nginx",
                      containerName: "nginx-production",
                      state: "running",
                      health: "healthy",
                    },
                  ],
                },
              ],
            },
          ],
          dataStale: false,
        };
      else if (p.endsWith("/files/text"))
        body = {
          path: "/hello.txt",
          text: "Design audit fixture",
          version: "test",
          size: 20,
        };
      else if (p.endsWith("/files"))
        body = {
          path: "/",
          entries: [
            {
              name: "hello.txt",
              path: "/hello.txt",
              kind: "file",
              size: 20,
              mode: 420,
              ownerUid: 0,
              ownerGid: 0,
              modifiedAt: 1710000000,
              version: "test",
            },
          ],
        };
      else if (p.endsWith("/history") && !p.includes("/alerts"))
        body = {
          nodeId: "node-production",
          from: new Date(Date.now() - 3600000).toISOString(),
          to: now,
          series: [
            {
              key: "cpu.usage_percent",
              points: [
                {
                  bucketAt: new Date(
                    Math.floor(Date.now() / 60000) * 60000 - 120000,
                  ).toISOString(),
                  value: 24,
                  samples: 1,
                  minimum: 24,
                  maximum: 24,
                },
                {
                  bucketAt: new Date(
                    Math.floor(Date.now() / 60000) * 60000 - 60000,
                  ).toISOString(),
                  value: 30,
                  samples: 1,
                  minimum: 30,
                  maximum: 30,
                },
              ],
            },
          ],
          resolution: "minute",
        };
      else if (p.endsWith("/setup/status")) {
        body = { initialized };
        if (unavailable) {
          status = 503;
          body = { error: "Core unavailable" };
        }
      } else if (p === "/api/v1/public/appearance")
        body = {
          displayName: "Alex",
          theme,
          backgroundColor: "#101827",
          avatarUrl: "",
          backgroundUrl: "",
        };
      else if (p.endsWith("/auth/me")) {
        body = { user: { displayName: "Alex" } };
        if (!signedIn) {
          status = 401;
          body = { error: "invalid credentials" };
        }
      } else if (p.endsWith("/auth/csrf")) body = { token: "preview-token" };
      else if (p.endsWith("/auth/login") || p.endsWith("/auth/setup")) {
        signedIn = true;
        initialized = true;
        body = { csrfToken: "preview-token" };
      } else if (p.endsWith("/auth/logout")) {
        signedIn = false;
        status = 204;
      } else if (p.endsWith("/sessions")) body = { sessions: [] };
      else if (p === "/api/v1/nodes")
        body = {
          nodes: populated
            ? nodes.map((n) => ({
                ...n,
                leaseValidUntil: new Date(Date.now() + 60000).toISOString(),
              }))
            : [],
          serverTime: now,
        };
      else if (p === "/api/v1/dashboard/settings") body = settings;
      else if (p.endsWith("/preferences")) body = { preferences: [] };
      else if (p.endsWith("/tasks")) body = { tasks: [], nextCursor: "" };
      else if (p.endsWith("/metrics")) {
        const n = nodes.find((n) => p.includes(n.nodeId));
        body = {
          type: "node_status",
          nodeId: n.nodeId,
          state: {
            nodeId: n.nodeId,
            status: n.status,
            generation: 1,
            serverTime: now,
            leaseValidUntil: new Date(Date.now() + 60000).toISOString(),
          },
        };
      } else if (p.endsWith("/containers")) {
        const n = nodes.find((n) => p.includes(n.nodeId));
        body = {
          type: "node_containers",
          nodeId: n.nodeId,
          state: {
            status: n.status,
            serverTime: now,
            generation: 1,
            leaseValidUntil: new Date(Date.now() + 60000).toISOString(),
          },
          inventory: {
            nodeId: n.nodeId,
            agentId: n.agentId,
            containers: [],
            dockerAvailability: "available",
            serverTime: now,
            activeGeneration: 1,
            agentOnline: n.status === "online",
            dataStale: n.status !== "online",
          },
          preferenceIdentities: {},
        };
      } else if (p === "/api/v1/settings/appearance") {
        Object.assign(body, JSON.parse(route.request().postData() || "{}"));
        theme = body.theme || theme;
        status = 204;
      } else if (p.includes("/alerts"))
        body = {
          alerts: [],
          rules: [],
          channels: [],
          windows: [],
          deliveries: [],
          events: [],
        };
      else if (p.includes("/probes")) body = { probes: [] };
      else if (p.includes("tailscale")) {
        if (discoveryAvailable)
          body = {
            peers: [
              {
                identity: "audit-peer",
                name: "Audit Linux host",
                os: "linux",
                class: "linux",
                online: true,
                ips: ["100.64.0.1"],
                managed: false,
              },
            ],
            discoveredAt: now,
          };
        else {
          status = 503;
          body = { error: "tailscale unavailable" };
        }
      }
      if (p.endsWith("/metrics") && p.includes("node-production")) {
        const metric = (value) => ({
          status: "known",
          value,
          sampledAt: now,
          sampleAgeMillis: 0,
        });
        body = {
          agentId: "agent-production",
          nodeId: "node-production",
          generation: 1,
          activeGeneration: 1,
          sequence: 1,
          nodeStatus: "online",
          collectedAt: now,
          receivedAt: now,
          serverTime: now,
          leaseValidUntil: new Date(Date.now() + 60000).toISOString(),
          clockOffsetMs: 0,
          metrics: {
            system: {
              hostname: metric("tokyo-production"),
              os: metric("linux"),
              architecture: metric("amd64"),
              platform: metric("Ubuntu"),
              platformFamily: metric("debian"),
              platformVersion: metric("24.04"),
              kernelVersion: metric("6.8.0"),
            },
            cpu: { usagePercent: metric(24.6), logicalCores: metric(8) },
            memory: metric({
              totalBytes: 17179869184,
              availableBytes: 10307921510,
              usedBytes: 6871947674,
              usedPercent: 40,
            }),
            network: {
              summary: metric({
                receivedBytesPerSecond: 128000,
                sentBytesPerSecond: 56000,
              }),
              interfaces: [],
            },
            disk: {
              status: "known",
              sampledAt: now,
              sampleAgeMillis: 0,
              mounts: [
                {
                  device: "/dev/sda1",
                  mountpoint: "/",
                  filesystem: "ext4",
                  usage: metric({
                    totalBytes: 107374182400,
                    availableBytes: 69793218560,
                    usedBytes: 37580963840,
                    usedPercent: 35,
                  }),
                },
              ],
            },
            host:{fastIntervalSeconds:5,diskIntervalSeconds:30,swap:metric({totalBytes:2147483648,usedBytes:104857600,usedPercent:4.88}),load:metric({one:0.42,five:0.31,fifteen:0.25}),processes:metric(187),sockets:metric({tcp:42,udp:15}),cpu:metric({model:"AMD EPYC 9654",physicalCores:4}),virtualization:metric("kvm/guest"),memory:metric({freeBytes:1234,cachedBytes:1073741824,bufferedBytes:10485760,sharedBytes:2048}),addresses:metric([{interface:"eth0",address:"192.0.2.24",family:4}]),publicIpv4:metric("192.0.2.24"),publicIpv6:{...metric(null),status:"unknown",reason:"unavailable"},gpus:metric([{id:"GPU-audit",name:"NVIDIA Test GPU",utilization:27,memoryUsedBytes:2147483648,memoryTotalBytes:8589934592,temperatureCelsius:61}]),traffic:{...metric({receivedBytes:1099511627776,sentBytes:107374182400,periodReceivedBytes:10737418240,periodSentBytes:1073741824,periodStart:now,coverageStart:now,lastSample:now,gaps:1}),reason:"partial_coverage"}},
            uptime: metric({ seconds: 1284600, bootId: "preview-boot-id" }),
          },
        };
      }
      if (p.endsWith("/containers") && p.includes("node-production")) {
        body.inventory.containers = ["nginx", "redis"].map((name, i) => ({
          generation: 1,
          sequence: i + 1,
          receivedAt: now,
          container: {
            id: "container-" + name,
            name: name + "-production",
            image: name + ":stable",
            imageId: "image-" + name,
            state: "running",
            running: true,
            paused: false,
            restarting: false,
            hostNetwork: false,
            health: "healthy",
            healthcheckConfigured: true,
            stale: false,
            restartCount: 0,
            ports: [],
            networks: [],
            mounts: [],
            observedAt: now,
          },
        }));
      }
      if (Object.keys(body).length === 0 && status !== 204)
        console.log("UNMOCKED", p);
      await route.fulfill({
        status,
        contentType: "application/json",
        body: status === 204 ? "" : JSON.stringify(body),
      });
    });
    await page.routeWebSocket("**/ws/**", (ws) => {
      if (!terminalOnly || !ws.url().includes("/streams/terminal")) return;
      terminalSocketCount++;
      const send = (action, text) => ws.send(JSON.stringify({type:"terminal",frame:{streamId:terminalID,action,...(text ? {data:Buffer.from(text).toString('base64')} : {})}}));
      if (terminalSocketCount === 1) {
        send("ready"); send("output","FIRST\r\n");
        setTimeout(() => ws.close({code:1012,reason:"isolated transport drop"}), 350);
      } else {
        send("ready"); send("output","\x1b[2J\x1b[HFIRST\r\nRESTORED\r\n");
      }
    });

    async function load() {
      await page.goto(webURL);
      await page.locator('[data-screen="nodes"]').waitFor();
      await page.locator(".dashboard-welcome,.vps-card").first().waitFor();
    }
    async function shot(name) {
      snapshots.push(name);
      await page.screenshot({ path: `${out}/${name}.png`, fullPage: true });
    }
    async function overflow() {
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth > innerWidth,
        ),
        false,
        "horizontal overflow on " +
          (await page.locator(".app-shell").getAttribute("data-screen")),
      );
    }
    async function invariants(label) {
      await page.waitForTimeout(120);
      await page.evaluate(() => document.fonts.ready);
      if (
        await page.evaluate(
          () => document.documentElement.scrollWidth > innerWidth,
        )
      ) {
        console.log(
          "OVERFLOW",
          label,
          await page.evaluate(() =>
            Array.from(document.querySelectorAll(".app-shell *"))
              .filter((el) => el.getBoundingClientRect().right > innerWidth + 1)
              .map((el) => ({
                tag: el.tagName,
                class: el.className,
                width: el.getBoundingClientRect().width,
                right: el.getBoundingClientRect().right,
              }))
              .slice(-12),
          ),
        );
        await shot("overflow-" + label);
      }
      await overflow();
      const bad = await page.evaluate(() =>
        Array.from(
          document.querySelectorAll(
            ".app-shell h1,.app-shell h2,.app-shell a,.app-shell button,.app-shell input:not([type=checkbox]):not([type=file]):not([type=color])",
          ),
        )
          .filter((el) => el.getBoundingClientRect().width > 0)
          .flatMap((el) => {
            const c = getComputedStyle(el);
            let problems = [];
            if (
              el.matches("h1,h2") &&
              c.fontWeight !== "400" &&
              !el.matches(".vps-card h2,.vps-group-heading")
            )
              problems.push("heading weight " + c.fontWeight);
            if (
              el.matches("a") &&
              ["rgb(0, 0, 238)", "rgb(0, 0, 255)", "rgb(85, 26, 139)"].includes(
                c.color,
              )
            )
              problems.push("browser link color");
            if (
              el.matches("button,input") &&
              parseFloat(c.minHeight) < 40 &&
              !el.matches(
                ".account-logout,.navigation-toggle,.terminal-close,.upload-button input",
              )
            )
              problems.push("control height " + c.minHeight);
            return problems.map(
              (p) => el.tagName + "." + el.className + ": " + p,
            );
          }),
      );
      assert.deepEqual(bad, [], label);
      await shot(label);
    }
    async function nav(label) {
      if (page.viewportSize().width <= 900 && await page.locator(".navigation-toggle").isVisible())
        await page.locator(".navigation-toggle").click();
      const key = Object.keys(enMessages).find(key => /^(nav\.|host\.title$|guest\.settings$)/.test(key) && enMessages[key] === label);
      assert.ok(key, 'Unknown navigation label: ' + label);
      const language = await page.locator('html').getAttribute('lang');
      const title = language === 'zh-CN' ? zhMessages[key] : enMessages[key];
      await page.locator('.workspace-nav').getByRole('button', {name:title, exact:true}).click();
      await page.waitForTimeout(250);
    }
    async function auditWorkspace(prefix) {
      await load();
      if(process.env.NODEDANCE_WEB_DESIGN_HOST_ONLY === "1") {
        await nav("Host management");
        await page.locator(".host-page").waitFor();
        for(let i=0;i<5;i++) {
          await page.locator(".host-tabs button").nth(i).click();
          await invariants(prefix+"-host-"+i);
        }
        return;
      }
      await invariants(prefix + "-nodes");
      const card = page.locator(".vps-card").first();
      assert.equal(await card.locator(".vps-card-docker").count(), 0);
      await card.locator(".docker-toggle").click();
      await card.locator(".vps-card-docker").waitFor();
      await invariants(prefix + "-nodes-docker-expanded");
      await card.locator(".docker-toggle").click();
      await card.locator(".vps-card-shortcuts button").first().click();
      await page.locator(".terminal-dialog").waitFor();
      assert.equal(await page.locator(".node-detail-page").count(), 0);
      await invariants(prefix + "-overview-terminal");
      await page.locator(".terminal-close").click();
      if (page.viewportSize().width > 900) {
        const handle = page.locator(".sidebar-resize");
        await handle.focus();
        await page.keyboard.press("End");
        assert.equal(await handle.getAttribute("aria-valuenow"), "400");
        const box = await handle.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + 100);
        await page.mouse.down();
        await page.mouse.move(box.x - 50, box.y + 100);
        await page.mouse.up();
        const width = await handle.getAttribute("aria-valuenow");
        assert(Number(width) < 400);
        await page.locator(".navigation-toggle").click();
        await page.locator(".workspace-sidebar.is-collapsed").waitFor();
        await invariants(prefix + "-sidebar-collapsed");
        await load();
        await page.locator(".workspace-sidebar.is-collapsed").waitFor();
        await page.locator(".navigation-toggle").click();
        assert.equal(await handle.getAttribute("aria-valuenow"), width);
        await invariants(prefix + "-sidebar-resized");
        await handle.focus();
        await page.keyboard.press("Home");
      }
      await page
        .locator(".vps-card")
        .first()
        .locator(".vps-name")
        .click();
      await page.locator(".node-detail-page").waitFor();
      assert.equal(await page.getByTestId("service-probes").count(), 0);
      await page.locator(".dashboard-tabs").getByRole("button", {name: /^(Service probes|服务探测)$/}).click();
      await page.locator(".probe-heading .probe-primary").click();
      await invariants(prefix + "-probe-form");
      await page.locator(".probe-form .probe-secondary").click();
      const terminalButton = page.locator(".node-terminal-entry button");
      if (await terminalButton.count()) {
        await terminalButton.click();
        await page.locator(".terminal-dialog").waitFor();
        await invariants(prefix + "-terminal");
        await page.locator(".terminal-close").click();
      }
      const tabs = await page.locator(".dashboard-tabs button").allTextContents();
      for (const label of tabs) {
        await page.locator(".dashboard-tabs").getByRole("button", {name: label, exact: true}).click();
        const section = new URLSearchParams(new URL(page.url()).hash.split("?")[1]).get("section") || "overview";
        await invariants(prefix + "-detail-" + section);
        if (section === "docker") {
          await page.locator(".node-terminal-entry button").click();
          await page.locator(".terminal-dialog").waitFor();
          await invariants(prefix + "-terminal");
          await page.locator(".terminal-close").focus();
          await page.keyboard.press("Escape");
          assert.equal(await page.locator(".terminal-dialog").count(), 0);
          await page.locator(".container-create-form summary").click();
          await invariants(prefix + "-container-create");
        }
        if (section === "compose") {
          await page.locator(".compose-create-form").waitFor();
          await invariants(prefix + "-compose-create");
          await page.locator(".compose-project-actions button").last().click();
          await page.locator(".compose-editor textarea").waitFor();
          await invariants(prefix + "-compose-editor");
        }
        if (section === "files") {
          await page.locator('.vuefinder').waitFor();
          const file = page.getByText('hello.txt', {exact:true}).first().locator('xpath=ancestor::div[contains(@class,"vf-explorer-item")][1]');
          await file.waitFor();
          await file.click({button:'right'});
          await page.getByText(/^(Delete|删除)$/, {exact:true}).last().click();
          await page.locator('.vuefinder__delete-modal__content').waitFor();
          await invariants(prefix + '-confirm-dialog');
          await page.keyboard.press('Escape');
          await page.locator('.vuefinder__modal-layout').waitFor({state:'detached'});
          await file.click({button:'right'});
          await page.getByText(/^(Rename|重命名)$/, {exact:true}).last().click();
          await page.locator('.vuefinder__rename-modal__input').waitFor();
          assert.equal(await page.locator('.vuefinder__rename-modal__input').inputValue(), 'hello.txt');
          await invariants(prefix + '-prompt-dialog');
          await page.keyboard.press('Escape');
          await page.locator('.vuefinder__modal-layout').waitFor({state:'detached'});
          await file.dblclick();
          await page.locator('.cm-content').waitFor();
          await invariants(prefix + '-file-editor');
          await page.keyboard.press('Escape');
          await page.locator('.vuefinder__modal-layout').waitFor({state:'detached'});
        }
        if (section === "rebuild") {
          const toggle = page.locator(".rebuild-entry>button").first();
          if (await toggle.count()) {
            await toggle.click();
            await invariants(prefix + "-rebuild-open");
          }
        }
      }
      for (const label of [
        "Host management",
        "Add an Agent",
        "Updates",
        "Operations",
        "Alerts",
        "Network discovery",
        "Settings",
      ]) {
        await nav(label);
        await invariants(
          prefix + "-" + label.toLowerCase().replaceAll(" ", "-"),
        );
        if(label === "Host management") {
          await page.locator(".host-page").waitFor();
          for(let i=0;i<5;i++) {
            await page.locator(".host-tabs button").nth(i).click();
            await invariants(prefix+"-host-"+i);
            await shot(prefix+"-host-"+i);
          }
        }
        if (label === "Updates") {
          await page.locator(".update-details a").waitFor();
          assert.equal(
            await page.locator(".update-confirm .primary-button").isDisabled(),
            false,
          );
          assert.equal(await page.locator('.update-confirm input[type=checkbox]').count(), 0, 'Ordinary update uses the shared direct apply operation');
          await invariants(prefix + "-release-details");
          await page.keyboard.press("Tab");
          await page.locator(".update-details a").focus();
          assert.notEqual(
            await page
              .locator(".update-details a")
              .evaluate((el) => getComputedStyle(el).outlineStyle),
            "none",
          );
        }
        if (label === "Add an Agent") {
          await page
            .locator(".agent-enrollment-form input")
            .nth(1)
            .fill("Audit node");
          await page.locator(".agent-enrollment-form .primary-button").click();
          await page.locator("[data-testid=agent-install-command]").waitFor();
          await page.locator("[data-testid=agent-deployment-method]").getByRole('button',{name:/^(Docker deployment|Docker 部署)$/}).click();
          await invariants(prefix + "-docker-command");
          await page.locator("[data-testid=agent-deployment-method]").getByRole('button',{name:/^(Direct installation \(systemd\)|直接安装（systemd）)$/}).click();
          await invariants(prefix + "-native-command");
        }
        if (label === "Network discovery") {
          discoveryAvailable = true;
          await page.locator(".discovery-heading button").click();
          await page.locator(".peer-card").waitFor();
          await page.locator(".peer-card button").click();
          await invariants(prefix + "-ssh-deploy");
          discoveryAvailable = false;
        }
        if (label === "Alerts") {
          for (let i = 0; i < 6; i++) {
            await page.locator(".alert-tabs button").nth(i).click();
            await invariants(prefix + "-alert-" + i);
          }
        }
      }
    }


    if (terminalOnly) {
      populated = true;
      await load();
      if (process.env.NODEDANCE_WEB_TERMINAL_LANG === "zh-CN") await page.locator('.language-selector').selectOption('zh-CN');
      for (const state of [
        {theme:"light",width:1440,height:1050}, {theme:"dark",width:1440,height:1050},
        {theme:"light",width:390,height:844}, {theme:"dark",width:390,height:844},
        {theme:"light",width:320,height:740}, {theme:"dark",width:320,height:740},
      ]) {
        theme=state.theme; terminalCreates=terminalResumes=terminalEnds=terminalSocketCount=0; resumeBodies.length=0;
        await page.setViewportSize(state);
        await load();
        await page.locator('.vps-card').first().locator('.vps-card-shortcuts button').first().click();
        await page.locator('.terminal-dialog').waitFor();
        await page.waitForFunction(() => document.querySelector('.terminal-heading p')?.textContent?.match(/reattaching|重新附着/));
        await invariants(`${theme}-${state.width}-terminal-reconnecting`);
        await page.waitForFunction(() => document.querySelector('.xterm-rows')?.textContent?.includes('RESTORED'));
        const contents = await page.locator('.xterm-rows').textContent();
        assert.equal((contents.match(/FIRST/g)||[]).length,1,'output replayed twice');
        assert.equal(terminalCreates,1,'recovery opened a new shell');
        assert.equal(terminalResumes,2,'transient resume failure was not retried');
        assert.deepEqual(resumeBodies,[{},{}]);
        assert.equal(await page.locator('.node-detail-page').count(),0);
        await invariants(`${theme}-${state.width}-terminal-restored`);
        await page.locator('.terminal-close').click();
        await page.waitForTimeout(100);
        assert(terminalEnds>0,'close did not terminate retained session');
      }
      assert.deepEqual(errors,[]);
      fs.writeFileSync(path.join(out,'terminal-report.json'),JSON.stringify({passed:true,snapshots},null,2)+'\n');
      console.log('Terminal recovery matrix passed: '+snapshots.length+' snapshots.');
      return;
    }
    await load();
    await invariants("empty-light");
    populated = true;
    const names = await page.locator(".workspace-nav button").allTextContents();
    console.log("Navigation:", names.map((s) => s.trim()).join(", "));
    for (const state of [
      { theme: "light", width: 1440, height: 1050 },
      { theme: "dark", width: 1440, height: 1050 },
      { theme: "light", width: 390, height: 844 },
      { theme: "dark", width: 390, height: 844 },
      { theme: "light", width: 320, height: 740 },
      { theme: "dark", width: 320, height: 740 },
    ]) {
      theme = state.theme;
      await page.setViewportSize(state);
      await auditWorkspace(theme + "-" + state.width);
      signedIn = false;
      await page.goto(webURL);
      await page.locator('[data-screen="login"]').waitFor();
      await invariants(theme + "-" + state.width + "-login");
      initialized = false;
      await page.goto(webURL);
      await page.locator('[data-screen="setup"]').waitFor();
      await invariants(theme + "-" + state.width + "-setup");
      initialized = true;
      signedIn = true;
    }
    await load();
    await page.locator(".language-selector").selectOption("zh-CN");
    for (const state of [
      { theme: "light", width: 1440, height: 1050 },
      { theme: "dark", width: 1440, height: 1050 },
      { theme: "light", width: 390, height: 844 },
      { theme: "dark", width: 390, height: 844 },
      { theme: "light", width: 320, height: 740 },
      { theme: "dark", width: 320, height: 740 },
    ]) {
      theme = state.theme;
      await page.setViewportSize(state);
      const prefix = "zh-" + theme + "-" + state.width;
      await auditWorkspace(prefix);
      signedIn = false;
      await page.goto(webURL);
      await page.locator('[data-screen="login"]').waitFor();
      await invariants(prefix + "-login");
      initialized = false;
      await page.goto(webURL);
      await page.locator('[data-screen="setup"]').waitFor();
      await invariants(prefix + "-setup");
      initialized = true;
      signedIn = true;
    }
    unavailable = true;
    await page.goto(webURL);
    await page.locator("[data-screen=unavailable]").waitFor();
    await invariants("core-unavailable");
    unavailable = false;
    let releaseBootstrap;
    const bootstrap = new Promise((resolve) => {
      releaseBootstrap = resolve;
    });
    await page.route("**" + catalog.operations.find(o => o.name === "auth.status").path, async (route) => {
      await bootstrap;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ initialized: true }),
      });
    });
    await page.evaluate(() => localStorage.setItem('nodedance.theme', 'system'));
    await page.emulateMedia({ colorScheme: 'light' });
    theme = 'system';
    await page.goto(webURL);
    await page.locator("[data-screen=loading]").waitFor();
    assert.equal(await page.locator('.app-shell').getAttribute('data-theme'), 'light');
    assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), 'light');
    await invariants("core-loading-system-light");
    releaseBootstrap();
    await page.locator("[data-screen=nodes]").waitFor();
    assert.equal(await page.locator('.app-shell').getAttribute('data-theme'), 'light');
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.waitForFunction(() => document.querySelector('.app-shell').dataset.theme === 'dark');
    await load();
    assert.equal(await page.locator('.app-shell').getAttribute('data-theme'), 'dark');
    assert.deepEqual(errors, []);
    fs.writeFileSync(
      path.join(out, "report.json"),
      JSON.stringify({ passed: true, snapshots }, null, 2) + "\n",
    );
    console.log(
      "Entire Web design matrix passed: " + snapshots.length + " snapshots.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
