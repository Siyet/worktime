import { expect, seedServer, test } from "./fixtures";

const ROUTES = ["/", "/projects", "/timeoff", "/reports", "/settings"];

test.describe("shell layout", () => {
  test("the header sits in the same place on every page", async ({ page, server }) => {
    // A day of entries on the Timer page and nothing much on Settings: the two
    // extremes for page height, which is what used to decide whether a scrollbar
    // existed and therefore where a centred shell landed.
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      projects: [{ id: crypto.randomUUID(), name: "Backend" }],
      entries: Array.from({ length: 12 }, (_, index) => ({
        description: `Task ${index}`,
        startedAt: todayNine + index * 600_000,
        stoppedAt: todayNine + index * 600_000 + 300_000,
      })),
    });
    await page.setViewportSize({ width: 1200, height: 700 });

    const boxes: { route: string; x: number; width: number }[] = [];
    for (const route of ROUTES) {
      await page.goto(server.url + "/#" + route);
      await expect(page.locator("header .logo")).toBeVisible();
      const box = await page.locator("header").boundingBox();
      boxes.push({ route, x: Math.round(box!.x), width: Math.round(box!.width) });
    }

    // Not "roughly the same": a shell that changes width, or a scrollbar that
    // comes and goes, moves the navigation under the cursor between clicks.
    const first = boxes[0]!;
    for (const box of boxes) {
      expect(box, `header moved on ${box.route}`).toEqual({ route: box.route, x: first.x, width: first.width });
    }
  });
});
