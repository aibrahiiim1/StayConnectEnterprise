package main

// Accessibility words in every shipped language; {n} in otp.valid is the minutes the server reported, never a number written here. They join builtinStrings at start-up.
var a11yStrings = map[string]map[string]string{
	"en": {
		"link.newtab": "(opens in a new tab)",
		"otp.valid":   "The code is valid for {n} min.",
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
		"otp.valid":   "Der Code ist {n} Min. lang gültig.",
		"otp.resend":  "Neuen Code senden",
		"otp.resent":  "Wir haben Ihnen einen neuen Code gesendet.",
	},
	"fr": {
		"link.newtab": "(s’ouvre dans un nouvel onglet)",
		"otp.valid":   "Le code est valable {n} min.",
		"otp.resend":  "Envoyer un nouveau code",
		"otp.resent":  "Nous vous avons envoyé un nouveau code.",
	},
	"it": {
		"link.newtab": "(si apre in una nuova scheda)",
		"otp.valid":   "Il codice è valido per {n} min.",
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

// A key defined twice is refused loudly.
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
