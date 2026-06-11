import { ConfigProvider } from "antd";
import React from "react";
import ReactDOM from "react-dom/client";

import "./index.css";
import Router from "./router";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <ConfigProvider
      theme={{
        token: {
          colorPrimary: "#294eb2",
        },
      }}
    >
      <Router />
    </ConfigProvider>
  </React.StrictMode>,
);
