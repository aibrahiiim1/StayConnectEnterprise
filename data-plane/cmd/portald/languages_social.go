package main

// The "Open in your browser" page in every shipped language; {p} is the provider's brand name, never translated.
var socialBrowserStrings = map[string]map[string]string{
	"en": {
		"social.browser.title":  "Open this page in your browser",
		"social.browser.lead":   "{p} does not allow signing in from this window. Open your browser (for example Chrome), go to the address below and sign in there.",
		"social.browser.copy":   "Copy the address",
		"social.browser.copied": "Copied",
		"social.browser.other":  "Use another way to sign in",
	},
	"ar": {
		"social.browser.title":  "افتح هذه الصفحة في متصفحك",
		"social.browser.lead":   "لا يسمح {p} بتسجيل الدخول من هذه النافذة. افتح متصفحك (مثل Chrome)، وانتقل إلى العنوان أدناه وسجّل الدخول هناك.",
		"social.browser.copy":   "نسخ العنوان",
		"social.browser.copied": "تم النسخ",
		"social.browser.other":  "استخدم طريقة أخرى لتسجيل الدخول",
	},
	"de": {
		"social.browser.title":  "Diese Seite im Browser öffnen",
		"social.browser.lead":   "{p} erlaubt keine Anmeldung aus diesem Fenster. Öffnen Sie Ihren Browser (zum Beispiel Chrome), rufen Sie die folgende Adresse auf und melden Sie sich dort an.",
		"social.browser.copy":   "Adresse kopieren",
		"social.browser.copied": "Kopiert",
		"social.browser.other":  "Auf andere Weise anmelden",
	},
	"fr": {
		"social.browser.title":  "Ouvrez cette page dans votre navigateur",
		"social.browser.lead":   "{p} ne permet pas de se connecter depuis cette fenêtre. Ouvrez votre navigateur (par exemple Chrome), allez à l’adresse ci-dessous et connectez-vous depuis celui-ci.",
		"social.browser.copy":   "Copier l’adresse",
		"social.browser.copied": "Copiée",
		"social.browser.other":  "Se connecter autrement",
	},
	"it": {
		"social.browser.title":  "Apri questa pagina nel browser",
		"social.browser.lead":   "{p} non consente l’accesso da questa finestra. Apri il browser (ad esempio Chrome), vai all’indirizzo qui sotto e accedi da lì.",
		"social.browser.copy":   "Copia l’indirizzo",
		"social.browser.copied": "Copiato",
		"social.browser.other":  "Accedi in un altro modo",
	},
	"ru": {
		"social.browser.title":  "Откройте эту страницу в браузере",
		"social.browser.lead":   "{p} не разрешает вход из этого окна. Откройте браузер (например, Chrome), перейдите по адресу ниже и войдите там.",
		"social.browser.copy":   "Скопировать адрес",
		"social.browser.copied": "Скопировано",
		"social.browser.other":  "Войти другим способом",
	},
}

// A key defined twice is refused loudly.
func init() {
	for code, words := range socialBrowserStrings {
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
