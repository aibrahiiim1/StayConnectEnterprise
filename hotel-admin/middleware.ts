import { NextRequest, NextResponse } from "next/server";

// sc_edge_session is HttpOnly, so only its presence is visible here; validity is checked via /edge/v1/auth/whoami.
// Redirects must be absolute (req.nextUrl.clone()): Next throws ERR_INVALID_URL on a relative Location. Behind Caddy
// the hotel.* vhost rewrites an absolute internal-host redirect to a relative Location, preserving the external host.
const BRAND_ASSET_PREFIXES = ["/favicon", "/apple-touch-icon", "/android-chrome", "/maskable-icon", "/site.webmanifest"];

export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has("sc_edge_session");

  const isLogin = pathname === "/login";
  const isPublic =
    isLogin ||
    pathname.startsWith("/_next") ||
    BRAND_ASSET_PREFIXES.some((p) => pathname.startsWith(p));

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
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico).*)"],
};
