# Sign in with Google for a Site — setup guide

Audience: the Site Admin. Design: [contract §5](../architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md).

**Your Site owns everything Google needs**: its Google Cloud project, its OAuth client and its portal domain. The
appliance signs your Clients in by itself. OneGate Central is not involved: it never sees a Client's Google identity, a
token or a session, and holds none of your Google credentials. (Its only part is obtaining the HTTPS certificate of your
portal domain, which carries no Client data.)

## Before you start

You need a domain of your own for the Client Portal (for example `wifi.example.com`) and a Google account that owns your
Google Cloud project and that domain in Google Search Console. OneGate's own names (`portal.onegate.getsemantics.com` and
the appliance's technical name) are not accepted by Google for your project, so Google sign-in always returns to your
domain. All your other sign-in methods keep using the shared portal address.

## 1. Give the Site its portal domain (Admin Console → Portal HTTPS)

1. Enter your domain, for example `wifi.example.com`.
2. Publish the three DNS records the page shows (a verification TXT and two CNAMEs) at the DNS provider of that domain.
3. Press **Check now**. When the domain shows *Verified* and a certificate is installed, the first row of the Google
   checklist (step 3) turns green. This normally takes a few minutes.

## 2. Create the OAuth client in your Google Cloud project

1. Google Cloud Console → **APIs & Services → Credentials → Create credentials → OAuth client ID**, type
   **Web application**.
2. **Authorized redirect URIs**: add exactly `https://<your domain>/auth/social/callback`
   (the Admin Console shows the exact text, with a copy button).
3. **Google Auth Platform → Branding**: set the app name, your home page and privacy policy on your domain, and add your
   domain under **Authorized domains**. Google requires the same Google account to own the domain in
   [Search Console](https://search.google.com/search-console).
4. **Audience**: choose *External* and press **Publish app** (*In production*). An app left in *Testing* is limited to
   100 named test users, and Clients see an unverified-app warning.

## 3. Configure it in the Admin Console (Sign-in → Social providers → Add → Google)

1. Paste the **Client ID** (it ends in `.apps.googleusercontent.com`) and the **Client secret**. The secret is stored
   encrypted on this appliance, can never be read back, and is never sent anywhere.
2. Tick the callback for your domain ("I registered this callback URL").
3. Switch **Offer this on the sign-in page** on and confirm with your password.

The **Google setup** checklist in the same dialog tells you what is still open. Items marked *?* are things only Google
can confirm (redirect URI, domain verification, publishing status); the appliance does not guess them.

## What your Clients see

On a phone, the sign-in sheet that opens when joining the Wi-Fi is an embedded browser, and Google refuses sign-in
inside it. The portal detects the sheet (Android WebView, the iOS captive-network sheet) and shows the portal address
to open in the normal browser, with a copy button. Android 12+ opens the portal in a real Chrome tab on its own.
In a normal browser the Client signs in with Google and returns to your domain on this appliance.

## If something fails

| Symptom | Cause | Fix |
|---|---|---|
| Google is not offered, "Google needs your portal domain" | No verified portal domain with a certificate | Section 1 |
| Google shows `redirect_uri_mismatch` | The redirect URI in the OAuth client is not exactly the one shown | Section 2, step 2 |
| Google shows "This app isn't verified" or limits users | The app is in *Testing*, or the domain is not an authorized domain | Section 2, steps 3 and 4 |
| `disallowed_useragent` | Sign-in was started inside an embedded sheet | Open the portal address in the browser |
| Sign-in is offered but fails after Google | The Client ID or secret do not belong together | Re-enter both from the same OAuth client |
