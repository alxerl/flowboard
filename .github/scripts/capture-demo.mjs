import { chromium } from 'playwright';

const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 980 }, deviceScaleFactor: 1 });
  await page.goto('http://localhost:8080/', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Explore demo workspace' }).click();
  await page.locator('.card').first().waitFor();
  await page.screenshot({ path: 'demo-screenshot.png', fullPage: true });
} finally {
  await browser.close();
}
