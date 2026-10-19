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
- Your device's network addresses (MAC and IP address), the name your device gives itself on the network while it is connected (often its owner's name), when you connect and disconnect, and how much data each session uses. Not the websites you visit.
- What you sign in with: [[delete the ways you do not offer]] your room number and the detail asked for with it (checked against your reservation); a voucher code; an account name; an email address and password; a phone number; or the Google, Apple, Facebook or Microsoft account you choose, from which we keep only your email address, your account id with that company and, for a work or school account, the identifiers of that organisation. When you ask for a code, the address and browser it was requested from.
- [[Only if you offer room sign-in:]] To check room sign-ins, a copy of the current reservations from our reservation system: the names of everyone on the reservation (including room sharers), room, room type, rate plan, reservation number, dates of stay, our reservation system's guest number, whether the stay is marked VIP, and the travel agent, if any.
- Sign-in attempts, including failed ones, to protect the network against misuse.
- If you buy internet access: the package, the price and the payment reference. Card details are handled by the payment provider, not by us. [[Only if you offer room charge:]] If you charge it to your room, the room, reservation number and our reservation system's guest number go on the bill, and stay with our financial records for as long as the law requires.

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
- [[Only if the sign-in page uses pictures or fonts from another website:]] Opening the sign-in page loads them from [[that website]], which then sees your device's IP address.
Some of these companies may process data outside [[the European Economic Area]]; [[the safeguard used, for example the European Commission's standard contractual clauses]].

HOW LONG WE KEEP IT
Reservation details: [[period]] after checkout. Device addresses and sessions: [[period]] after the device was last seen. Email addresses, phone numbers, sign-in accounts and Wi-Fi accounts: [[period]] after they were last used. Sign-in attempts: [[period]]. After that they are deleted or made anonymous. Payment and room-charge records: as long as the law requires. Our backups keep a copy for up to [[backup period]], so something already deleted can remain in a backup until it expires.

COOKIES
The sign-in page uses these cookies, and no advertising or tracking cookies:
- sc-lang: the language you choose, for a year.
- og_client: if you sign in with a code or an online account over a secure (https) connection, remembers this device so you can reconnect with one tap, for [[period]]. Signing out, or "Not you?", forgets it.
- og_resume: if you continue without signing in, lets this device come back to the access it started, for 30 days.
- og_account: keeps you signed in to your account page, for 15 minutes.
- sc_commerce: holds your package choice while you finish signing in or paying, for 10 minutes.

YOUR RIGHTS
You can ask to see your data, receive a copy, correct it, have it deleted, restrict its use, or object to it. Ask at reception or write to [[privacy contact email]]. You can also complain to [[your data protection authority, with its website]].

Last updated: [[date]]`;
