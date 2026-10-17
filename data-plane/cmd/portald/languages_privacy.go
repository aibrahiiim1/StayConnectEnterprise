package main

// THE PRIVACY NOTICE'S OWN WORDS, IN EVERY SHIPPED LANGUAGE (GDPR remediation plan F1 and F7).
//
// The notice itself is the hotel's text (privacy_text in the design), in whatever language the hotel wrote it.
// These are the portal's words around it: the line that opens it, the link to a full notice, and the sentence
// that explains the remembered-device cookie (og_client, client_resume.go). They join builtinStrings at start-up
// like helpStrings, so there is still one dictionary.
var privacyStrings = map[string]map[string]string{
	"en": {
		"privacy.link":     "How we use your data",
		"privacy.url":      "Privacy notice",
		"privacy.remember": "If you sign in with a code or an online account, this device is remembered with a cookie so you can reconnect with one tap. Signing out, or “Not you?”, forgets it.",
	},
	"ar": {
		"privacy.link":     "كيف نستخدم بياناتك",
		"privacy.url":      "إشعار الخصوصية",
		"privacy.remember": "إذا سجّلت الدخول برمز أو بحساب على الإنترنت، يُحفظ هذا الجهاز بملف تعريف ارتباط (كوكي) لتتمكن من إعادة الاتصال بلمسة واحدة. تسجيل الخروج أو «لست أنت؟» يلغي حفظه.",
	},
	"de": {
		"privacy.link":     "Wie wir Ihre Daten verwenden",
		"privacy.url":      "Datenschutzhinweis",
		"privacy.remember": "Wenn Sie sich mit einem Code oder einem Online-Konto anmelden, wird dieses Gerät mit einem Cookie gespeichert, damit Sie sich mit einem Tippen erneut verbinden können. Abmelden oder „Nicht Sie?“ löscht es.",
	},
	"fr": {
		"privacy.link":     "Comment nous utilisons vos données",
		"privacy.url":      "Politique de confidentialité",
		"privacy.remember": "Si vous vous connectez avec un code ou un compte en ligne, cet appareil est mémorisé à l'aide d'un cookie pour vous reconnecter d'un seul geste. Se déconnecter, ou « Ce n'est pas vous ? », l'efface.",
	},
	"it": {
		"privacy.link":     "Come usiamo i tuoi dati",
		"privacy.url":      "Informativa sulla privacy",
		"privacy.remember": "Se accedi con un codice o con un account online, questo dispositivo viene ricordato tramite un cookie, così puoi riconnetterti con un tocco. Disconnettendoti, o con «Non sei tu?», viene dimenticato.",
	},
	"ru": {
		"privacy.link":     "Как мы используем ваши данные",
		"privacy.url":      "Уведомление о конфиденциальности",
		"privacy.remember": "Если вы входите с кодом или через онлайн-аккаунт, это устройство запоминается с помощью cookie, чтобы вы могли подключиться снова одним нажатием. Выход из системы или «Не вы?» удаляет его.",
	},
}

// The privacy words join the one dictionary at start-up; a key defined twice is refused loudly.
func init() {
	for code, words := range privacyStrings {
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
