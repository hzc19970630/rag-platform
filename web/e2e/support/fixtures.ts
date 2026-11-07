import { test as base, expect, type APIRequestContext } from "@playwright/test";
import { newApi } from "./api";

export const test = base.extend<{ api: APIRequestContext }>({
  api: async ({}, use) => {
    const api = await newApi();
    try {
      await use(api);
    } finally {
      await api.dispose();
    }
  },
});

export { expect };
