import { MotionConfig } from "framer-motion";
import { Suspense } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "@/components/arc/foundation.css";
import "./style.css";
import "./figma-components.css";
createRoot(document.getElementById("app")!).render(
  <MotionConfig reducedMotion="user">
    <Suspense fallback={<div role="status">Loading…</div>}>
      <App />
    </Suspense>
  </MotionConfig>,
);

import "./components/ArcTheme.css";
