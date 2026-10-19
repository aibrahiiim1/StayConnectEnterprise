package main

// THE PRIVACY NOTICE'S OWN WORDS, IN EVERY SHIPPED LANGUAGE (GDPR remediation plan F1 and F7).
//
// The notice itself is the hotel's text (privacy_text in the design), in whatever language the hotel wrote it.
// These are the portal's words around it: the line that opens it, the link to a full notice, the sentence
// that explains the remembered-device cookie (og_client, client_resume.go), and privacy.default, the short
// notice shown while the hotel has written none (GDPR audit 2026-10-19, A5): a guest is always told who is
// responsible and what is recorded before signing in (Art. 13). They join builtinStrings at start-up
// like helpStrings, so there is still one dictionary.
var privacyStrings = map[string]map[string]string{
	"en": {
		"privacy.default":  "This Wi-Fi is provided by the hotel, which is responsible for your personal data on it. To give you internet access and keep the network secure, it records your device’s network addresses, when you connect and how much data you use, and what you sign in with. These records are deleted or made anonymous after a set time. Ask at reception for the hotel’s full privacy notice, or to see, correct or delete your data.",
		"privacy.link":     "How we use your data",
		"privacy.url":      "Privacy notice",
		"privacy.remember": "If you sign in with a code or an online account, this device is remembered with a cookie so you can reconnect with one tap. Signing out, or “Not you?”, forgets it.",
	},
	"ar": {
		"privacy.default":  "يقدّم الفندق شبكة الواي فاي هذه، وهو المسؤول عن بياناتك الشخصية عليها. ولتوفير الإنترنت لك والحفاظ على أمان الشبكة، يسجّل عناوين جهازك على الشبكة، ووقت اتصالك، وكمية البيانات التي تستخدمها، وما تسجّل الدخول به. تُحذف هذه السجلات أو تُجعل مجهولة الهوية بعد مدة محددة. اطلب من الاستقبال إشعار الخصوصية الكامل للفندق، أو الاطلاع على بياناتك أو تصحيحها أو حذفها.",
		"privacy.link":     "كيف نستخدم بياناتك",
		"privacy.url":      "إشعار الخصوصية",
		"privacy.remember": "إذا سجّلت الدخول برمز أو بحساب على الإنترنت، يُحفظ هذا الجهاز بملف تعريف ارتباط (كوكي) لتتمكن من إعادة الاتصال بلمسة واحدة. تسجيل الخروج أو «لست أنت؟» يلغي حفظه.",
	},
	"de": {
		"privacy.default":  "Dieses WLAN stellt das Hotel bereit, das für Ihre personenbezogenen Daten darin verantwortlich ist. Um Ihnen den Internetzugang zu geben und das Netz zu schützen, speichert es die Netzwerkadressen Ihres Geräts, wann Sie sich verbinden, wie viele Daten Sie nutzen und womit Sie sich anmelden. Diese Daten werden nach einer festgelegten Zeit gelöscht oder anonymisiert. Den vollständigen Datenschutzhinweis des Hotels erhalten Sie an der Rezeption; dort können Sie auch Auskunft, Berichtigung oder Löschung Ihrer Daten verlangen.",
		"privacy.link":     "Wie wir Ihre Daten verwenden",
		"privacy.url":      "Datenschutzhinweis",
		"privacy.remember": "Wenn Sie sich mit einem Code oder einem Online-Konto anmelden, wird dieses Gerät mit einem Cookie gespeichert, damit Sie sich mit einem Tippen erneut verbinden können. Abmelden oder „Nicht Sie?“ löscht es.",
	},
	"fr": {
		"privacy.default":  "Ce Wi-Fi est fourni par l’hôtel, responsable de vos données personnelles sur ce réseau. Pour vous donner accès à Internet et protéger le réseau, il enregistre les adresses réseau de votre appareil, l’heure de vos connexions, la quantité de données utilisée et le moyen de connexion choisi. Ces données sont supprimées ou rendues anonymes après une durée déterminée. Demandez à la réception la politique de confidentialité complète de l’hôtel, ou l’accès à vos données, leur rectification ou leur suppression.",
		"privacy.link":     "Comment nous utilisons vos données",
		"privacy.url":      "Politique de confidentialité",
		"privacy.remember": "Si vous vous connectez avec un code ou un compte en ligne, cet appareil est mémorisé à l'aide d'un cookie pour vous reconnecter d'un seul geste. Se déconnecter, ou « Ce n'est pas vous ? », l'efface.",
	},
	"it": {
		"privacy.default":  "Questo Wi-Fi è fornito dall’hotel, titolare dei tuoi dati personali su questa rete. Per darti accesso a Internet e proteggere la rete, registra gli indirizzi di rete del tuo dispositivo, quando ti connetti, quanti dati utilizzi e con cosa accedi. Questi dati vengono cancellati o resi anonimi dopo un periodo stabilito. Chiedi alla reception l’informativa completa dell’hotel, oppure di vedere, correggere o cancellare i tuoi dati.",
		"privacy.link":     "Come usiamo i tuoi dati",
		"privacy.url":      "Informativa sulla privacy",
		"privacy.remember": "Se accedi con un codice o con un account online, questo dispositivo viene ricordato tramite un cookie, così puoi riconnetterti con un tocco. Disconnettendoti, o con «Non sei tu?», viene dimenticato.",
	},
	"ru": {
		"privacy.default":  "Этот Wi-Fi предоставляет отель, который отвечает за ваши персональные данные в этой сети. Чтобы дать вам доступ в интернет и защитить сеть, он записывает сетевые адреса вашего устройства, время подключения, объём использованных данных и способ входа. Эти записи удаляются или обезличиваются через установленный срок. Полное уведомление о конфиденциальности отеля можно получить на стойке регистрации; там же можно попросить показать, исправить или удалить ваши данные.",
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
