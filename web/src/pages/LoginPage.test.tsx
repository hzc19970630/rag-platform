import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { LoginPage } from "./LoginPage";

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ldap login form", () => {
  it("renders the directory form when LDAP is enabled", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/auth/config")) {
        return new Response(JSON.stringify({
          oidcEnabled: false,
          oidcIssuer: "",
          ldapEnabled: true,
          devAuthEnabled: false,
          sessionCookie: "rag_session",
          sessionTTLHours: 1,
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ error: "unauthenticated" }), { status: 401 });
    }));

    renderLogin();

    expect(await screen.findByPlaceholderText(i18n.t("auth.username"))).toBeInTheDocument();
    expect(screen.getByPlaceholderText(i18n.t("auth.password"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: i18n.t("auth.ldapLogin") })).toBeInTheDocument();
  });
});
