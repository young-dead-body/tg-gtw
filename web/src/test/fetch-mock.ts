import { vi } from "vitest";

export interface MockRoute {
  method: string;
  path: string;
  status: number;
  body: unknown;
}

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function mockFetch(routes: MockRoute[]) {
  const fn = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? String(input) : input.url;
      const path = url.replace(/^https?:\/\/[^/]+/, "").replace(/\/$/, "");
      const method = init?.method ?? "GET";
      const route = routes.find((r) => r.method === method && r.path === path);
      if (!route) {
        return jsonResponse(404, { error: "not found" });
      }
      return jsonResponse(route.status, route.body);
    },
  );
  vi.stubGlobal("fetch", fn);
  return fn;
}
