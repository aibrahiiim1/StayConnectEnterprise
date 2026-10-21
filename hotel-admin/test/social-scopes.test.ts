import { describe, expect, it } from "vitest";
import { DEFAULT_SCOPES, extraScopes } from "@/lib/social-scopes";

// GDPR plan C9 (D-C9): Hotel Admin warns, and does not refuse, when a hotel asks a sign-in provider for more than
// the guest's name and email.
describe("social sign-in scopes", () => {
  it("does not warn about the defaults or about name and email", () => {
    for (const [p, s] of Object.entries(DEFAULT_SCOPES)) expect(extraScopes(p, s)).toEqual([]);
    expect(extraScopes("google", "openid email profile")).toEqual([]);
    expect(extraScopes("facebook", "email,public_profile")).toEqual([]);
    expect(extraScopes("apple", "name email")).toEqual([]);
    expect(extraScopes("google", "")).toEqual([]);
  });

  it("names each extra scope once", () => {
    expect(extraScopes("facebook", "email, user_birthday user_friends user_birthday")).toEqual(["user_birthday", "user_friends"]);
    expect(extraScopes("google", "openid email https://www.googleapis.com/auth/user.birthday.read"))
      .toEqual(["https://www.googleapis.com/auth/user.birthday.read"]);
    expect(extraScopes("microsoft", "openid email profile User.Read.All")).toEqual(["User.Read.All"]);
  });
});
