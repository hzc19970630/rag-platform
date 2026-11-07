import { request } from "@playwright/test";
import { apiBase, apiOrigin, devHeaders } from "./support/api";

export default async function globalSetup(): Promise<void> {
  const webURL = process.env.E2E_BASE_URL ?? "http://localhost:5173/";
  const context = await request.newContext({ timeout: 10_000 });
  try {
    const ready = await context.get(`${apiOrigin}/readyz`);
    if (!ready.ok()) {
      throw new Error(
        `API is not ready at ${apiOrigin}/readyz (${ready.status()}). Start the quickstart stack first: ./scripts/quickstart.sh`,
      );
    }

    const authResponse = await context.get(`${apiBase}/auth/config`);
    if (!authResponse.ok()) {
      throw new Error(`auth config returned ${authResponse.status()} at ${apiBase}/auth/config`);
    }
    const auth = (await authResponse.json()) as { devAuthEnabled?: boolean };
    if (!auth.devAuthEnabled) {
      throw new Error("development identity is disabled; run the quickstart overlay so DEV_AUTH_ENABLED=true");
    }

    const me = await context.get(`${apiBase}/me`, { headers: devHeaders });
    if (!me.ok()) {
      throw new Error(`development identity was rejected by /me (${me.status()})`);
    }

    const web = await context.get(webURL);
    if (!web.ok()) {
      throw new Error(`web UI is not reachable at ${webURL} (${web.status()})`);
    }
  } finally {
    await context.dispose();
  }
}
