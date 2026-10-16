package main

// A CLIENT'S DEVICES, IN EVERY SHIPPED LANGUAGE (device_pages.go, docs/architecture/ONEGATE_DEVICE_MANAGEMENT.md).
//
// The online page's "Your devices" panel (its older dev.* words live in languages_pages.go) and the page a client
// reaches when a package's devices are all in use: choose the device this one replaces. {n}, {max} are numbers.
// These join builtinStrings at start-up like the other dictionaries, so the page renders them, /api/languages
// serves them, and Hotel Admin offers every key for override.
var deviceStrings = map[string]map[string]string{
	"en": {
		"dev.slots":          "{n} of {max} devices in use",
		"dev.changes":        "You can change devices {n} more times today.",
		"dev.changes.none":   "You've reached today's limit for changing devices. Please contact the site team for assistance.",
		"dev.confirm.online": "Disconnect device {n} and free its place? It goes offline now. Your time and data stay the same.",
		"dev.this":           "This device",
		"rep.title":          "Choose a device to replace",
		"rep.lead":           "Your package allows {max} devices and all of them are in use. To connect this device, choose the one it replaces: that device is disconnected and gives up its place. Your remaining time and data stay the same.",
		"rep.btn":            "Use this device instead",
		"rep.unavailable":    "Your devices can't be shown right now. Please sign in again.",
		"pkg.signout":        "Not you? Sign out",
		"pkg.voucher.title":  "Have a voucher code from the site team? Enter it here",
		"pkg.voucher.btn":    "Use voucher",
	},
	"ar": {
		"dev.slots":          "{n} من {max} أجهزة قيد الاستخدام",
		"dev.changes":        "يمكنك تغيير الأجهزة {n} مرات أخرى اليوم.",
		"dev.changes.none":   "لقد بلغت حد تغيير الأجهزة لهذا اليوم. يرجى التواصل مع فريق الموقع للمساعدة.",
		"dev.confirm.online": "فصل الجهاز {n} وتحرير مكانه؟ سيصبح غير متصل الآن. يبقى وقتك وبياناتك كما هي.",
		"dev.this":           "هذا الجهاز",
		"rep.title":          "اختر جهازًا لاستبداله",
		"rep.lead":           "تسمح باقتك بـ {max} أجهزة وجميعها قيد الاستخدام. لتوصيل هذا الجهاز، اختر الجهاز الذي سيحل محله: سيُفصل ذلك الجهاز ويتخلى عن مكانه. يبقى وقتك وبياناتك المتبقية كما هي.",
		"rep.btn":            "استخدم هذا الجهاز بدلًا منه",
		"rep.unavailable":    "لا يمكن عرض أجهزتك الآن. يرجى تسجيل الدخول مرة أخرى.",
		"pkg.signout":        "لست أنت؟ تسجيل الخروج",
		"pkg.voucher.title":  "لديك رمز قسيمة من فريق الموقع؟ أدخله هنا",
		"pkg.voucher.btn":    "استخدام القسيمة",
	},
	"de": {
		"dev.slots":          "{n} von {max} Geräten in Verwendung",
		"dev.changes":        "Sie können heute noch {n}-mal Geräte wechseln.",
		"dev.changes.none":   "Sie haben das heutige Limit für Gerätewechsel erreicht. Bitte wenden Sie sich an das Team vor Ort.",
		"dev.confirm.online": "Gerät {n} trennen und seinen Platz freigeben? Es geht jetzt offline. Ihre Zeit und Ihr Datenvolumen bleiben gleich.",
		"dev.this":           "Dieses Gerät",
		"rep.title":          "Wählen Sie ein Gerät, das ersetzt wird",
		"rep.lead":           "Ihr Paket erlaubt {max} Geräte, und alle sind in Verwendung. Um dieses Gerät zu verbinden, wählen Sie das Gerät, das es ersetzt: Es wird getrennt und gibt seinen Platz ab. Ihre verbleibende Zeit und Ihr Datenvolumen bleiben gleich.",
		"rep.btn":            "Stattdessen dieses Gerät verwenden",
		"rep.unavailable":    "Ihre Geräte können gerade nicht angezeigt werden. Bitte melden Sie sich erneut an.",
		"pkg.signout":        "Nicht Sie? Abmelden",
		"pkg.voucher.title":  "Haben Sie einen Gutscheincode vom Team vor Ort? Geben Sie ihn hier ein",
		"pkg.voucher.btn":    "Gutschein verwenden",
	},
	"fr": {
		"dev.slots":          "{n} appareils sur {max} utilisés",
		"dev.changes":        "Vous pouvez encore changer d’appareil {n} fois aujourd’hui.",
		"dev.changes.none":   "Vous avez atteint la limite de changements d’appareil pour aujourd’hui. Veuillez contacter l’équipe sur place.",
		"dev.confirm.online": "Déconnecter l’appareil {n} et libérer sa place ? Il passe hors ligne maintenant. Votre temps et vos données restent les mêmes.",
		"dev.this":           "Cet appareil",
		"rep.title":          "Choisissez l’appareil à remplacer",
		"rep.lead":           "Votre forfait autorise {max} appareils et tous sont utilisés. Pour connecter cet appareil, choisissez celui qu’il remplace : cet appareil est déconnecté et libère sa place. Votre temps et vos données restants ne changent pas.",
		"rep.btn":            "Utiliser cet appareil à la place",
		"rep.unavailable":    "Vos appareils ne peuvent pas être affichés pour le moment. Veuillez vous reconnecter.",
		"pkg.signout":        "Ce n’est pas vous ? Se déconnecter",
		"pkg.voucher.title":  "Vous avez un code de bon de l’équipe sur place ? Saisissez-le ici",
		"pkg.voucher.btn":    "Utiliser le bon",
	},
	"it": {
		"dev.slots":          "{n} dispositivi su {max} in uso",
		"dev.changes":        "Puoi cambiare dispositivo ancora {n} volte oggi.",
		"dev.changes.none":   "Hai raggiunto il limite di cambi di dispositivo per oggi. Contatta lo staff della struttura.",
		"dev.confirm.online": "Disconnettere il dispositivo {n} e liberare il suo posto? Andrà offline ora. Il tuo tempo e i tuoi dati restano invariati.",
		"dev.this":           "Questo dispositivo",
		"rep.title":          "Scegli il dispositivo da sostituire",
		"rep.lead":           "Il tuo pacchetto consente {max} dispositivi e sono tutti in uso. Per connettere questo dispositivo, scegli quello che sostituisce: verrà disconnesso e cederà il suo posto. Il tempo e i dati rimanenti restano invariati.",
		"rep.btn":            "Usa invece questo dispositivo",
		"rep.unavailable":    "Non è possibile mostrare i tuoi dispositivi ora. Accedi di nuovo.",
		"pkg.signout":        "Non sei tu? Esci",
		"pkg.voucher.title":  "Hai un codice voucher dallo staff? Inseriscilo qui",
		"pkg.voucher.btn":    "Usa il voucher",
	},
	"ru": {
		"dev.slots":          "Используется устройств: {n} из {max}",
		"dev.changes":        "Сегодня вы можете сменить устройство ещё {n} раз(а).",
		"dev.changes.none":   "Вы достигли сегодняшнего лимита смены устройств. Обратитесь, пожалуйста, к персоналу.",
		"dev.confirm.online": "Отключить устройство {n} и освободить его место? Оно сразу отключится от сети. Ваше время и трафик не изменятся.",
		"dev.this":           "Это устройство",
		"rep.title":          "Выберите устройство для замены",
		"rep.lead":           "Ваш пакет допускает {max} устройств(а), и все они заняты. Чтобы подключить это устройство, выберите то, которое оно заменит: оно будет отключено и освободит место. Оставшиеся время и трафик не изменятся.",
		"rep.btn":            "Использовать это устройство вместо него",
		"rep.unavailable":    "Сейчас не удаётся показать ваши устройства. Пожалуйста, войдите снова.",
		"pkg.signout":        "Не вы? Выйти",
		"pkg.voucher.title":  "Есть код ваучера от персонала? Введите его здесь",
		"pkg.voucher.btn":    "Использовать ваучер",
	},
}

func init() {
	for code, words := range deviceStrings {
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
