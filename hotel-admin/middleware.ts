import { NextRequest, NextResponse } from "next/server";

// Cookie sc_edge_session is HttpOnly, so this middleware only sees its
// presence. A real validity check happens via /edge/v1/auth/whoami on the
// client.
//
// Redirects use req.nextUrl.clone() (an absolute URL) + NextResponse.redirect.
// A *relative* Location header is NOT viable: current Next.js normalises the
// middleware response via `new URL(location)`, which throws ERR_INVALID_URL on
// a relative string (that was the cause of the /login?next=… 500). Behind Caddy
// the hotel.* vhost carries a `header_down Location "^https?://localhost:3100…"`
// rewrite, so any absolute redirect to the internal bind host is rewritten to a
// relative Location for the browser — the external host is preserved.
export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has("sc_edge_session");

  const isLogin = pathname === "/login";
  const isPublic = isLogin || pathname.startsWith("/_next") || pathname.startsWith("/favicon");

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
