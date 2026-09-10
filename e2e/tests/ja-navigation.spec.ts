import { expect, test } from '@playwright/test';
import LoginPage from '../pages/LoginPage';
import ViewPage from '../pages/ViewPage';
import TreeView from '../pages/TreeView';
import AddPageDialog from '../pages/AddPageDialog';
import EditPage from '../pages/EditPage';
import DeletePageDialog from '../pages/DeletePageDialog';
import { toAppPath } from '../pages/appPath';

test('Japanese wiki navigation preserves CRUD, history and system routes', async ({ page }) => {
  const login = new LoginPage(page);
  await login.goto();
  expect(new URL(page.url()).pathname).toBe(toAppPath('/login'));
  await login.login(
    process.env.E2E_ADMIN_USER || 'admin',
    process.env.E2E_ADMIN_PASSWORD || 'admin',
  );
  const view = new ViewPage(page);
  await view.expectUserLoggedIn();
  await page.goto(toAppPath('/ja/'));
  await expect(page.locator('article')).toBeVisible();
  await expect(page).toHaveURL(/\/ja\//);

  const tree = new TreeView(page);
  await tree.clickRootAddButton();
  const title = `Ja Navigation ${Date.now()}`;
  const slug = title.toLowerCase().replaceAll(' ', '-');
  const dialog = new AddPageDialog(page);
  await dialog.fillTitle(title);
  const created = page.waitForResponse(
    (r) => new URL(r.url()).pathname.endsWith('/api/pages') && r.request().method() === 'POST',
  );
  await dialog.submitWithoutRedirect();
  const current = await (await created).json();
  expect(current.path).toBe(slug); // API/domain paths do not gain a language segment.
  await expect(await tree.findPageByTitle(title)).toHaveAttribute('href', toAppPath(`/ja/${slug}`));
  await tree.clickPageByTitle(title);
  expect(new URL(page.url()).pathname).toBe(toAppPath(`/ja/${slug}`));
  await view.clickEditPageButton();
  await expect(page).toHaveURL(new RegExp(`/ja/e/${slug}$`));
  const editor = new EditPage(page);
  await editor.writeContent('\nJapanese route content');
  await editor.savePage();
  await editor.closeEditor();
  await expect(page).toHaveURL(new RegExp(`/ja/${slug}$`));
  await expect(page.locator('article')).toContainText('Japanese route content');
  await page.getByTestId(`favorite-toggle-${current.id}`).first().click();
  await expect(
    page.getByTestId('favorites-section').locator('a', { hasText: title }),
  ).toHaveAttribute('href', toAppPath(`/ja/${slug}`));
  await view.openCurrentPageHistory();
  await expect(page).toHaveURL(new RegExp(`/ja/history/${slug}$`));
  await view.expectRevisionListVisible();
  await view.goto(slug);
  await view.switchToSearchTab();
  await page.getByTestId('search-input').fill(title);
  const result = page.getByTestId(`search-result-card-${current.id}`);
  await expect(result).toHaveAttribute('href', new RegExp(`/ja/${slug}(\\?|$)`));
  await result.click();
  await expect(page.locator('article')).toContainText('Japanese route content');

  const cookies = await page.context().cookies();
  const csrf = cookies.find((c) => c.name.endsWith('leafwiki_csrf'));
  expect(csrf).toBeDefined();
  const headers = { 'X-CSRF-Token': decodeURIComponent(csrf!.value) };
  const create = await page.request.post(toAppPath('/api/pages'), {
    headers,
    data: { title: 'Reference', slug: `${slug}-ref`, kind: 'page', parentId: current.id },
  });
  expect(create.ok()).toBeTruthy();
  const ref = await create.json();
  const update = await page.request.put(toAppPath(`/api/pages/${ref.id}`), {
    headers,
    data: {
      version: ref.version,
      title: ref.title,
      slug: ref.slug,
      content: `[absolute](/${slug})\n[relative](..)\n[[${title}]]`,
    },
  });
  expect(update.ok()).toBeTruthy();
  await view.goto(ref.path);
  await expect(page.locator('.breadcrumbs-nav__link')).toHaveAttribute(
    'href',
    toAppPath(`/ja/${slug}`),
  );
  for (const name of ['absolute', 'relative', title]) {
    await expect(page.locator('article').getByRole('link', { name, exact: true })).toHaveAttribute(
      'href',
      toAppPath(`/ja/${slug}`),
    );
  }
  await page.locator('article').getByRole('link', { name: 'absolute', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/ja/${slug}$`));
  await expect(
    page
      .locator(`a[href="${toAppPath(`/ja/${ref.path}`)}"]`)
      .filter({ hasText: 'Reference' })
      .last(),
  ).toBeVisible();
  await page.goto(toAppPath(`/ja/p/${current.id}/${slug}`));
  await expect(page).toHaveURL(new RegExp(`/ja/${slug}$`));
  expect((await page.request.get(toAppPath('/api/health'))).ok()).toBeTruthy();
  expect((await page.request.get(toAppPath(`/${slug}`))).status()).toBe(404);
  await page.goto(toAppPath('/settings'));
  await expect(page).toHaveURL(/\/settings\//);
  await view.goto(ref.path);
  await view.clickDeletePageButton();
  await new DeletePageDialog(page).confirmDeletion();
  await expect(page).toHaveURL(new RegExp(`/ja/${slug}$`));
  await view.logout();
  await expect(page).toHaveURL(/\/login/);
});
