import { NextRequest, NextResponse } from "next/server";

// The HttpOnly cookie's presence is only a hint; whoami verifies the session.
export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has("sc_session");

  const isLogin = pathname === "/login";
  const isPublic = isLogin || pathname.startsWith("/_next") || pathname.startsWith("/favicon") || pathname.startsWith("/icon");

  if (!hasSession && !isPublic) {
    const url = req.nextUrl.clone();
    url.pathname = "/login";
    url.searchParams.set("next", pathname);
    return NextResponse.redirect(url);
  }
  // Expired sessions stay on sign-in even while the browser still holds a stale cookie.
  if (hasSession && isLogin && req.nextUrl.searchParams.get("reason") !== "expired") {
    const url = req.nextUrl.clone();
    url.pathname = "/overview";
    url.search = "";
    return NextResponse.redirect(url);
  }
  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico).*)"],
};
