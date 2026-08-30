import { describe, expect, it } from "vitest";
import { CLIENT_BUILD_VERSION } from "./buildVersion";

describe("client build identity", () => {
  it("uses the version injected into the frontend build", () => {
    expect(CLIENT_BUILD_VERSION).toBe("v9.8.7-test");
    expect(CLIENT_BUILD_VERSION).not.toBe("dev");
  });
});
