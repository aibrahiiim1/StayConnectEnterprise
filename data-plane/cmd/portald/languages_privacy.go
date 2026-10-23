package main

// The portal's words around the hotel's privacy notice, including privacy.default shown while the hotel has written none, so a guest is always told who is responsible and what is recorded before signing in (Art. 13).
var privacyStrings = map[string]map[string]string{
	"en": {
		"privacy.default":  "This Wi-Fi is provided by the hotel, which is responsible for your personal data on it. To give you internet access and keep the network secure, it records your device’s network addresses, when you connect and how much data you use, and what you sign in with. These records are deleted or made anonymous after a set time. Ask at reception for the hotel’s full privacy notice, or to see, correct or delete your data.",
		"privacy.link":     "How we use your data",
		"privacy.url":      "Privacy notice",
		"privacy.remember": "If you tick “Remember this device”, a cookie keeps it remembered so you can reconnect with one tap. Signing out, or “Not you?”, forgets it.",
		"remember.device":  "Remember this device, so I can reconnect with one tap",
	},
	"ar": {
		"privacy.default":  "يقدّم الفندق شبكة الواي فاي هذه، وهو المسؤول عن بياناتك الشخصية عليها. ولتوفير الإنترنت لك والحفاظ على أمان الشبكة، يسجّل عناوين جهازك على الشبكة، ووقت اتصالك، وكمية البيانات التي تستخدمها، وما تسجّل الدخول به. تُحذف هذه السجلات أو تُجعل مجهولة الهوية بعد مدة محددة. اطلب من الاستقبال إشعار الخصوصية الكامل للفندق، أو الاطلاع على بياناتك أو تصحيحها أو حذفها.",
		"privacy.link":     "كيف نستخدم بياناتك",
		"privacy.url":      "إشعار الخصوصية",
		"privacy.remember": "إذا اخترت «تذكّر هذا الجهاز»، يحفظه ملف تعريف ارتباط (كوكي) لتتمكن من إعادة الاتصال بلمسة واحدة. تسجيل الخروج أو «لست أنت؟» يلغي حفظه.",
		"remember.device":  "تذكّر هذا الجهاز لأعيد الاتصال بلمسة واحدة",
	},
	"de": {
		"privacy.default":  "Dieses WLAN stellt das Hotel bereit, das für Ihre personenbezogenen Daten darin verantwortlich ist. Um Ihnen den Internetzugang zu geben und das Netz zu schützen, speichert es die Netzwerkadressen Ihres Geräts, wann Sie sich verbinden, wie viele Daten Sie nutzen und womit Sie sich anmelden. Diese Daten werden nach einer festgelegten Zeit gelöscht oder anonymisiert. Den vollständigen Datenschutzhinweis des Hotels erhalten Sie an der Rezeption; dort können Sie auch Auskunft, Berichtigung oder Löschung Ihrer Daten verlangen.",
		"privacy.link":     "Wie wir Ihre Daten verwenden",
		"privacy.url":      "Datenschutzhinweis",
		"privacy.remember": "Wenn Sie „Dieses Gerät merken“ ankreuzen, speichert ein Cookie das Gerät, damit Sie sich mit einem Tippen erneut verbinden können. Abmelden oder „Nicht Sie?“ löscht es.",
		"remember.device":  "Dieses Gerät merken, damit ich mich mit einem Tippen erneut verbinden kann",
	},
	"fr": {
		"privacy.default":  "Ce Wi-Fi est fourni par l’hôtel, responsable de vos données personnelles sur ce réseau. Pour vous donner accès à Internet et protéger le réseau, il enregistre les adresses réseau de votre appareil, l’heure de vos connexions, la quantité de données utilisée et le moyen de connexion choisi. Ces données sont supprimées ou rendues anonymes après une durée déterminée. Demandez à la réception la politique de confidentialité complète de l’hôtel, ou l’accès à vos données, leur rectification ou leur suppression.",
		"privacy.link":     "Comment nous utilisons vos données",
		"privacy.url":      "Politique de confidentialité",
		"privacy.remember": "Si vous cochez « Mémoriser cet appareil », un cookie le mémorise pour vous reconnecter d'un seul geste. Se déconnecter, ou « Ce n'est pas vous ? », l'efface.",
		"remember.device":  "Mémoriser cet appareil pour me reconnecter d'un seul geste",
	},
	"it": {
		"privacy.default":  "Questo Wi-Fi è fornito dall’hotel, titolare dei tuoi dati personali su questa rete. Per darti accesso a Internet e proteggere la rete, registra gli indirizzi di rete del tuo dispositivo, quando ti connetti, quanti dati utilizzi e con cosa accedi. Questi dati vengono cancellati o resi anonimi dopo un periodo stabilito. Chiedi alla reception l’informativa completa dell’hotel, oppure di vedere, correggere o cancellare i tuoi dati.",
		"privacy.link":     "Come usiamo i tuoi dati",
		"privacy.url":      "Informativa sulla privacy",
		"privacy.remember": "Se selezioni «Ricorda questo dispositivo», un cookie lo ricorda così puoi riconnetterti con un tocco. Disconnettendoti, o con «Non sei tu?», viene dimenticato.",
		"remember.device":  "Ricorda questo dispositivo, per riconnettermi con un tocco",
	},
	"ru": {
		"privacy.default":  "Этот Wi-Fi предоставляет отель, который отвечает за ваши персональные данные в этой сети. Чтобы дать вам доступ в интернет и защитить сеть, он записывает сетевые адреса вашего устройства, время подключения, объём использованных данных и способ входа. Эти записи удаляются или обезличиваются через установленный срок. Полное уведомление о конфиденциальности отеля можно получить на стойке регистрации; там же можно попросить показать, исправить или удалить ваши данные.",
		"privacy.link":     "Как мы используем ваши данные",
		"privacy.url":      "Уведомление о конфиденциальности",
		"privacy.remember": "Если вы отметите «Запомнить это устройство», cookie запомнит его, чтобы вы могли подключиться снова одним нажатием. Выход из системы или «Не вы?» удаляет его.",
		"remember.device":  "Запомнить это устройство, чтобы подключаться одним нажатием",
	},
}

// A key defined twice is refused loudly.
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
