import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./style.css";
import "./styles/agents.css";
import "./styles/conversation.css";
import "./styles/navigation.css";
import "./styles/layout.css";
import { inDesktopShell, installExternalLinks } from "./lib/rpc";

installExternalLinks();
// Shell-specific chrome (the macOS traffic-light inset) keys off this.
document.documentElement.dataset.shell = inDesktopShell() ? "desktop" : "web";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
