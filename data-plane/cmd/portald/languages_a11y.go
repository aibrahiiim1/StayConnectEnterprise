package main

// THE ACCESSIBILITY WORDS, IN EVERY SHIPPED LANGUAGE (WCAG 2.2 AA remediation).
//
// link.newtab is read after a link that opens a new tab (the privacy notice and the terms), visually hidden:
// a screen-reader user is told before the tab changes under them. otp.valid tells a guest how long the code
// they were sent stays usable -- {n} is the minutes the server reported for that code, never a number written
// here. otp.resend and otp.resent are the code step's "Send a new code" and its confirmation. They join
// builtinStrings at start-up like privacyStrings, so there is still one dictionary.
var a11yStrings = map[string]map[string]string{
	"en": {
		"link.newtab": "(opens in a new tab)",
		"otp.valid":   "The code is valid for {n} minutes.",
		"otp.resend":  "Send a new code",
		"otp.resent":  "We sent you a new code.",
	},
	"ar": {
		"link.newtab": "(يُفتح في علامة تبويب جديدة)",
		"otp.valid":   "الرمز صالح لمدة {n} دقيقة.",
		"otp.resend":  "إرسال رمز جديد",
		"otp.resent":  "أرسلنا إليك رمزًا جديدًا.",
	},
	"de": {
		"link.newtab": "(öffnet in einem neuen Tab)",
		"otp.valid":   "Der Code ist {n} Minuten lang gültig.",
		"otp.resend":  "Neuen Code senden",
		"otp.resent":  "Wir haben Ihnen einen neuen Code gesendet.",
	},
	"fr": {
		"link.newtab": "(s’ouvre dans un nouvel onglet)",
		"otp.valid":   "Le code est valable {n} minutes.",
		"otp.resend":  "Envoyer un nouveau code",
		"otp.resent":  "Nous vous avons envoyé un nouveau code.",
	},
	"it": {
		"link.newtab": "(si apre in una nuova scheda)",
		"otp.valid":   "Il codice è valido per {n} minuti.",
		"otp.resend":  "Invia un nuovo codice",
		"otp.resent":  "Ti abbiamo inviato un nuovo codice.",
	},
	"ru": {
		"link.newtab": "(откроется в новой вкладке)",
		"otp.valid":   "Код действителен {n} мин.",
		"otp.resend":  "Отправить новый код",
		"otp.resent":  "Мы отправили вам новый код.",
	},
}

// The accessibility words join the one dictionary at start-up; a key defined twice is refused loudly.
func init() {
	for code, words := range a11yStrings {
		dst := builtinStrings[code]
		if dst == nil {
			dst = map[string]string{}
			builtinStrings[code] = dst
		}
		for k, v := range words {
			if _, dup := dst[k]; dup {
				panic("portald: translation key defined twice: " + code + "/" + k)
			}
			dst[k] = v
		}
	}
}
