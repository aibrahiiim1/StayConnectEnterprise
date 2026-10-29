package main

// AD-35: what a guest reads when the appliance is busy. "busy.moment" is the notice shown on the page while a request is
// slow (the portal has already retried once for them); "err.busy" is the sentence after the second try also found the
// appliance busy. Both exist in every shipped portal language.

const msgServiceBusy = "The service is busy right now. Please try again in a moment."

var busyStrings = map[string]map[string]string{
	"en": {
		"busy.moment": "One moment…",
		"err.busy":    msgServiceBusy,
	},
	"ar": {
		"busy.moment": "لحظة من فضلك…",
		"err.busy":    "الخدمة مشغولة حالياً. يرجى المحاولة مرة أخرى بعد قليل.",
	},
	"de": {
		"busy.moment": "Einen Moment bitte …",
		"err.busy":    "Der Dienst ist gerade ausgelastet. Bitte versuchen Sie es gleich noch einmal.",
	},
	"fr": {
		"busy.moment": "Un instant…",
		"err.busy":    "Le service est très sollicité en ce moment. Veuillez réessayer dans un instant.",
	},
	"it": {
		"busy.moment": "Un momento…",
		"err.busy":    "Il servizio è molto richiesto in questo momento. Riprova tra un attimo.",
	},
	"ru": {
		"busy.moment": "Минуту…",
		"err.busy":    "Сервис сейчас перегружен. Повторите попытку через минуту.",
	},
}

func init() {
	for code, words := range busyStrings {
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
	serverMessageKeys[msgServiceBusy] = "err.busy"
}
