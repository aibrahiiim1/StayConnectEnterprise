import { NextRequest, NextResponse } from "next/server";

// Cookie sc_session is HttpOnly, so the browser middleware only sees its
// presence. A real validity check happens via /v1/auth/whoami on the client.
//
// Redirects use RELATIVE Location headers: as a Next.js standalone server
// behind Caddy, req.nextUrl carries the internal bind host (localhost:3000), so
// an absolute redirect would send the browser to https://localhost:3000/... .
// A relative Location resolves against the host the browser actually used.
function relativeRedirect(location: string): NextResponse {
  return new NextResponse(null, { status: 307, headers: { Location: location } });
}

export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has("sc_session");

  const isLogin = pathname === "/login";
  const isPublic = isLogin || pathname.startsWith("/_next") || pathname.startsWith("/favicon");

  if (!hasSession && !isPublic) {
    return relativeRedirect(`/login?next=${encodeURIComponent(pathname)}`);
  }
  if (hasSession && isLogin) {
    return relativeRedirect(`/dashboard`);
  }
  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico).*)"],
};
