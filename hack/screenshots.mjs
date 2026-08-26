// Drives a headless Chromium over the Renfild web UI and writes one PNG per
// page and theme. Invoked by hack/screenshots.sh, which starts the server.
import { mkdirSync } from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), ".tools/screenshots/"));
const puppeteer = require(path.resolve(".tools/screenshots/node_modules/puppeteer-core"));

const BASE = process.env.BASE ?? "http://127.0.0.1:8080";
const OUT = process.env.OUT ?? "assets/screenshots";
const CHROME = process.env.CHROME;

const PAGES = [
  { name: "dashboard", path: "/dashboard" },
  { name: "speakers", path: "/speakers" },
  // The enrollment wizard only appears once a speaker is picked.
  { name: "enrollment", path: "/speakers", click: "Enroll" },
  { name: "intents", path: "/intents" },
  { name: "history", path: "/history" },
  { name: "settings", path: "/settings" },
];

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

mkdirSync(OUT, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "new",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
});

try {
  for (const theme of ["light", "dark"]) {
    const page = await browser.newPage();
    await page.setViewport({ width: 1440, height: 960, deviceScaleFactor: 2 });
    // The app stores the theme choice; set it before the first paint.
    await page.evaluateOnNewDocument((value) => {
      localStorage.setItem("renfild-theme", value);
    }, theme);

    for (const target of PAGES) {
      await page.goto(BASE + target.path, { waitUntil: "networkidle2" });
      await sleep(700); // let the polled data land
      if (target.click) {
        const clicked = await page.evaluate((label) => {
          const button = [...document.querySelectorAll("button")].find((element) =>
            element.textContent?.trim().startsWith(label),
          );
          button?.click();
          return Boolean(button);
        }, target.click);
        if (!clicked) throw new Error(`no "${target.click}" button on ${target.path}`);
        await sleep(700);
      }
      const suffix = theme === "dark" ? "-dark" : "";
      const file = path.join(OUT, `${target.name}${suffix}.png`);
      await page.screenshot({ path: file, fullPage: true });
      console.log(`  ${file}`);
    }
    await page.close();
  }
} finally {
  await browser.close();
}
