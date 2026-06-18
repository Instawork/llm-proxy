export const ADMIN_BASENAME = "/admin";

/** Path inside the SPA router (basename stripped), e.g. `/keys`. */
export function adminAppPath(
  pathname: string = window.location.pathname,
  search: string = window.location.search,
): string {
  let path = pathname;
  if (path === ADMIN_BASENAME || path === `${ADMIN_BASENAME}/`) {
    path = "/";
  } else if (path.startsWith(`${ADMIN_BASENAME}/`)) {
    path = path.slice(ADMIN_BASENAME.length);
  }
  if (!path.startsWith("/")) {
    path = `/${path}`;
  }
  return `${path}${search}`;
}

/** Full browser URL for an in-app route, e.g. `http://localhost:5173/admin/keys`. */
export function adminAbsoluteUrl(appPath: string): string {
  const path = appPath.startsWith("/") ? appPath : `/${appPath}`;
  if (path === "/" || path.startsWith("/?")) {
    return `${window.location.origin}${ADMIN_BASENAME}/${path.slice(1)}`;
  }
  return `${window.location.origin}${ADMIN_BASENAME}${path}`;
}

export function adminLoginRedirectUrl(appPath?: string): string {
  const target = encodeURIComponent(appPath ?? adminAppPath());
  return `${ADMIN_BASENAME}/login?redirect=${target}`;
}
