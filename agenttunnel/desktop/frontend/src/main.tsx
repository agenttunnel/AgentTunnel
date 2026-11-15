import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { installExternalLinks } from "./lib/rpc";

installExternalLinks();

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
