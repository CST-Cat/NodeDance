import { MotionConfig } from "framer-motion";
import { lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { initI18n } from "./i18n";
import "@/components/arc/foundation.css";
import "./style.css";
import "./figma-components.css";
const PublicFileShare=lazy(()=>import("./components/FileShares").then(module=>({default:module.PublicFileShare})));
const PublicFileRequest=lazy(()=>import("./components/FileRequests").then(module=>({default:module.PublicFileRequest})));
await initI18n();
createRoot(document.getElementById("app")!).render(
  <MotionConfig reducedMotion="user">
    <Suspense fallback={<div role="status">Loading…</div>}>
      {location.pathname.startsWith("/s/") ? <PublicFileShare /> : location.pathname.startsWith("/r/") ? <PublicFileRequest /> : <App />}
    </Suspense>
  </MotionConfig>,
);

import "./components/ArcTheme.css";
