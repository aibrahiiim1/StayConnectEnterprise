// THE PRIVACY NOTICE TEMPLATE (GDPR remediation plan F1, Product-Owner decision D-F1).
//
// The hotel is the controller and the notice is the hotel's: we ship a starting text, it completes it. The text
// says only what this system actually records. Everything that differs from hotel to hotel -- who the controller
// is, how long each record is kept, which outside services are switched on -- is a [[placeholder]]; the server
// warns (portaldesign, privacy_text) while any is left, so an unfinished notice is never published unnoticed.
//
// An engineering draft: the hotel's own legal adviser should review it before it is relied on (plan D-F3).

export const PRIVACY_TEMPLATE = `Privacy notice: guest Wi-Fi

WHO IS RESPONSIBLE
[[Hotel legal name]], [[address]], is responsible for your personal data on this Wi-Fi network (the "controller"). Contact: [[privacy contact email]]. [[If you have a data protection officer: their name and email. Otherwise delete this sentence.]]

WHAT WE RECORD
- Your device's network addresses (MAC and IP address), the name your device gives itself on the network while it is connected, when you connect and disconnect, and how much data each session uses. Not the websites you visit.
- What you sign in with: [[delete the ways you do not offer]] your room number and the detail asked for with it (checked against your reservation); a voucher code; an account name; an email address and password; a phone number; or the Google, Apple, Facebook or Microsoft account you choose, from which we keep only your email address and your account id with that company.
- [[Only if you offer room sign-in:]] To check room sign-ins, a copy of the current reservations from our reservation system: name, room, reservation number and dates of stay.
- Sign-in attempts, including failed ones, to protect the network against misuse.
- If you buy internet access: the package, the price and the payment reference. Card details are handled by the payment provider, not by us.

WHY
- To give you internet access and the package you chose (to perform our contract with you).
- To keep the network secure and stop misuse (our legitimate interest).
- [[Only if a law in your country requires you to keep connection records: name the law. Otherwise delete this line.]]

WHO ELSE RECEIVES IT
The Wi-Fi system runs on equipment in this hotel. [[Name of the Wi-Fi system supplier]] maintains it for us and may see these records only to do so.
[[Delete the lines that do not apply:]]
- Codes sent by email, text message or WhatsApp pass through the company that delivers them ([[SendGrid / Twilio / Meta]]).
- If you sign in with Google, Apple, Facebook or Microsoft, that company learns that you signed in here.
- If you pay by card, the payment provider ([[Stripe / Paymob]]) processes the payment.
Some of these companies may process data outside [[the European Economic Area]]; [[the safeguard used, for example the European Commission's standard contractual clauses]].

HOW LONG WE KEEP IT
Reservation details: [[period]] after checkout. Device addresses and sessions: [[period]] after the device was last seen. Email addresses, phone numbers, sign-in accounts and Wi-Fi accounts: [[period]] after they were last used. Sign-in attempts: [[period]]. After that they are deleted or made anonymous.

COOKIES
The sign-in page remembers the language you choose. If you sign in with a code or an online account, this device is also remembered with a cookie so you can reconnect with one tap; signing out, or "Not you?", forgets it.

YOUR RIGHTS
You can ask to see your data, receive a copy, correct it, have it deleted, restrict its use, or object to it. Ask at reception or write to [[privacy contact email]]. You can also complain to [[your data protection authority, with its website]].

Last updated: [[date]]`;
