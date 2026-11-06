import { ApiError } from "@/lib/api";

export type ResetPolicy = {
  enabled: boolean;
  code_minutes: number;
  max_attempts: number;
  account_per_hour: number;
  address_per_hour: number;
  allow_last_site_admin: boolean;
  config_version: number;
  updated_at?: string | null;
  updated_by?: string | null;
};

export type ResetNumberKey = "code_minutes" | "max_attempts" | "account_per_hour" | "address_per_hour";

export type ResetBound = {
  key: ResetNumberKey;
  label: string;
  unit: string;
  min: number;
  max: number;
  default: number;
  explains: string;
};

export type ResetMail = { known: boolean; ready: boolean; kind: string };

export type ResetPolicyAnswer = { settings: ResetPolicy; bounds: ResetBound[]; mail: ResetMail };

export function resetMailWords(mail: ResetMail): string | null {
  if (!mail.known) return "The appliance could not say whether an SMTP mail server is set up, so forgot password may not be offered at sign-in.";
  if (mail.ready) return null;
  if (mail.kind === "sendgrid") {
    return "Email goes out through SendGrid, not the site's own SMTP mail server. Password reset codes are sent only through the site's own SMTP server, so forgot password is off at sign-in.";
  }
  return "No SMTP mail server is set up, so forgot password is off at sign-in. Set up the site's own SMTP server so staff can reset a forgotten password themselves.";
}

export function resetSummary(p: ResetPolicy, mail: ResetMail): string {
  if (!p.enabled) return "Off";
  if (!mail.ready) return "On, but off at sign-in until an SMTP mail server is set up";
  return `On, codes work for ${p.code_minutes} min`;
}

export function resetSettingsError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "reauth_required" || e.status === 401) return "Your password was not accepted. Nothing was changed.";
    if (e.code === "version_conflict") return "Someone changed these settings since you opened them. Reload them and try again. Nothing was changed.";
    if (e.code === "invalid_setting" || e.code === "reason_required") return `${e.message.replace(/\.?$/, ".")} Nothing was changed.`;
    if (e.status === 403) return "Your role cannot change these settings. Nothing was changed.";
    if (e.status === 429) return "Too many tries in a short time. Wait a minute, then try again. Nothing was changed.";
  }
  return "The settings could not be saved. Check the appliance is reachable, then try again. Nothing was changed.";
}

export function resetRequestError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "reset_unavailable" && e.status === 409) {
      return "Password reset by email is not available on this appliance. Ask a site admin to reset your password under Operators.";
    }
    if (e.status === 429) return e.message;
    if (e.code === "bad_request") return "Enter the email address of your account.";
  }
  return "Your request could not be sent. Check your connection, then try again.";
}

export type ConfirmProblem = { field: "code" | "password" | "mfa" | "form"; message: string };

export function resetConfirmError(e: unknown): ConfirmProblem {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "code_refused":
        return { field: "code", message: "The code is wrong or has expired. Check it, or ask for a new code. A code stops working after too many wrong tries." };
      case "password_weak":
        return { field: "password", message: e.message.replace(/\.?$/, ".") };
      case "mfa_required":
        return { field: "mfa", message: "This account uses an authenticator app. Enter the 6-digit code it shows now, or a recovery code." };
      case "mfa_wrong":
        return { field: "mfa", message: "That authenticator code is not right. Enter the 6-digit code your app shows now, or a recovery code." };
      case "reset_unavailable":
        if (e.status === 409) {
          return { field: "form", message: "Password reset by email is not available on this appliance. Ask a site admin to reset your password under Operators." };
        }
        break;
      case "too_many_attempts":
        return { field: "form", message: e.message };
    }
  }
  return { field: "form", message: "Your password could not be changed. Check your connection, then try again. Nothing was changed." };
}
