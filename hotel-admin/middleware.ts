import { NextRequest, NextResponse } from "next/server";

// sc_edge_session is HttpOnly, so only its presence is visible here; validity is checked via /edge/v1/auth/whoami.
// Redirects must be absolute (req.nextUrl.clone()): Next throws ERR_INVALID_URL on a relative Location. Behind Caddy
// the hotel.* vhost rewrites an absolute internal-host redirect to a relative Location, preserving the external host.
export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has("sc_edge_session");

  const isLogin = pathname === "/login";
  const isPublic =
    isLogin ||
    pathname.startsWith("/_next") ||
    pathname.startsWith("/favicon") ||
    pathname.startsWith("/icon"); // app/icon.svg — the tab favicon, must not redirect

  if (!hasSession && !isPublic) {
    const url = req.nextUrl.clone();
    url.pathname = "/login";
    url.searchParams.set("next", pathname);
    return NextResponse.redirect(url);
  }
  if (hasSession && isLogin) {
    const url = req.nextUrl.clone();
    url.pathname = "/dashboard";
    url.search = "";
    return NextResponse.redirect(url);
  }
  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico|icon.svg).*)"],
};
