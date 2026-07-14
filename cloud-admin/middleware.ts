import { NextRequest, NextResponse } from "next/server";

// Cookie sc_session is HttpOnly, so the browser middleware only sees its
// presence. A real validity check happens via /v1/auth/whoami on the client.
export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has("sc_session");

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
