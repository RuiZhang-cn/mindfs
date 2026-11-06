import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { ActionBar } from "../src/components/ActionBar";
import { bootstrapService } from "../src/services/bootstrap";
import { e2eeService } from "../src/services/e2ee";
import { I18nProvider } from "../src/i18n";
import "../src/index.css";

bootstrapService.canUseProtectedAPI = () => true;
e2eeService.protectedFetch = (input, init) => fetch(input, init);
e2eeService.parseProtectedJSONResponse = response => response.json();

function Fixture() {
  const [rootId, setRootId] = useState<string | null>("attachments");
  return <I18nProvider>
    <button onClick={() => setRootId(null)}>Clear project</button>
    <ActionBar status="connected" currentRootId={rootId}
      onNewSession={() => {}} onSelectProject={() => {}} onSelectSession={() => {}} />
  </I18nProvider>;
}

createRoot(document.getElementById("root")!).render(<Fixture />);
