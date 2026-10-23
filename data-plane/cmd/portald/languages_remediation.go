package main

// Server sentences that reached guests in English whatever their language, plus words of the pages added by the portal audit; every handler-composed sentence is registered in serverMessageKeys so localise() translates it.

const (
	msgPleaseSignIn          = "Please sign in."
	msgCannotDetectDevice    = "We couldn't detect your device. Please reconnect to the Wi-Fi and try again."
	msgTooManyAttemptsMoment = "Too many attempts. Please wait a moment and try again."
	msgFreeUsedNothingElse   = "You've used your free access here, and no other package is available for you right now. Please contact the site team."
	// Device-limit sentences for paths that cannot offer the replacement page: signing out does not free a place, so the way is "Your devices" on an online device or the site team.
	msgVoucherDevices = "Every device place on this voucher is in use. On a device that is online with it, remove one under “Your devices”, or contact the site team."
	msgAccountDevices = "Every device place on this account is in use. On a device that is online with it, remove one under “Your devices”, or contact the site team."
)

var remediationStrings = map[string]map[string]string{
	"en": {
		"err.signin":           msgPleaseSignIn,
		"err.device.reconnect": msgCannotDetectDevice,
		"err.attempts.moment":  msgTooManyAttemptsMoment,
		"err.free.used":        msgFreeUsedNothingElse,
		"err.poststay":         guestPostStayMessage,
		"err.timeout.title":    "This is taking longer than expected",
		"err.timeout":          "We did not get an answer in time. If you were connecting, check whether your device is online before you try again.",
		"leave.title":          "Disconnect this device?",
		"leave.lead":           "Tap Disconnect to sign this device out of the Wi-Fi.",
		"pay.other":            "Choose another way to get online",
		"room.failed":          "The room charge was not completed. Nothing was charged for internet access. You can choose another way to get online.",
		"room.review":          "We could not confirm the room charge automatically. The site team will check it; please contact them if your room was charged.",
	},
	"ar": {
		"err.signin":           "يرجى تسجيل الدخول.",
		"err.device.reconnect": "تعذّر التعرّف على جهازك. يرجى إعادة الاتصال بشبكة Wi-Fi ثم المحاولة مرة أخرى.",
		"err.attempts.moment":  "محاولات كثيرة جداً. يرجى الانتظار لحظة ثم المحاولة مرة أخرى.",
		"err.free.used":        "لقد استخدمت الوصول المجاني هنا، ولا توجد باقة أخرى متاحة لك حالياً. يرجى التواصل مع فريق الموقع.",
		"err.poststay":         "تعذّر التحقق من إقامتك. يرجى التحقق من بياناتك أو التواصل مع فريق الموقع للمساعدة.",
		"err.timeout.title":    "يستغرق الأمر وقتاً أطول من المتوقع",
		"err.timeout":          "لم نتلقَّ رداً في الوقت المناسب. إذا كنت تتصل، فتحقق مما إذا كان جهازك متصلاً قبل المحاولة مرة أخرى.",
		"leave.title":          "فصل هذا الجهاز؟",
		"leave.lead":           "اضغط «قطع الاتصال» لتسجيل خروج هذا الجهاز من شبكة Wi-Fi.",
		"pay.other":            "اختر طريقة أخرى للاتصال",
		"room.failed":          "لم تكتمل إضافة المبلغ إلى الغرفة. لم يتم خصم أي مبلغ مقابل الإنترنت. يمكنك اختيار طريقة أخرى للاتصال.",
		"room.review":          "لم نتمكن من تأكيد إضافة المبلغ إلى الغرفة تلقائياً. سيتحقق فريق الموقع من ذلك؛ يرجى التواصل معهم إذا أُضيف المبلغ إلى غرفتك.",
	},
	"de": {
		"err.signin":           "Bitte melden Sie sich an.",
		"err.device.reconnect": "Ihr Gerät konnte nicht erkannt werden. Bitte verbinden Sie sich erneut mit dem WLAN und versuchen Sie es noch einmal.",
		"err.attempts.moment":  "Zu viele Versuche. Bitte warten Sie einen Moment und versuchen Sie es dann erneut.",
		"err.free.used":        "Sie haben Ihren kostenlosen Zugang hier bereits genutzt, und derzeit ist kein anderes Paket für Sie verfügbar. Bitte wenden Sie sich an das Team vor Ort.",
		"err.poststay":         "Ihr Aufenthalt konnte nicht bestätigt werden. Bitte prüfen Sie Ihre Angaben oder wenden Sie sich an das Team vor Ort.",
		"err.timeout.title":    "Das dauert länger als erwartet",
		"err.timeout":          "Wir haben nicht rechtzeitig eine Antwort erhalten. Wenn Sie sich gerade verbunden haben, prüfen Sie zuerst, ob Ihr Gerät online ist.",
		"leave.title":          "Dieses Gerät trennen?",
		"leave.lead":           "Tippen Sie auf „Trennen“, um dieses Gerät vom WLAN abzumelden.",
		"pay.other":            "Einen anderen Weg ins Internet wählen",
		"room.failed":          "Die Zimmerbelastung wurde nicht abgeschlossen. Für den Internetzugang wurde nichts berechnet. Sie können einen anderen Weg wählen.",
		"room.review":          "Wir konnten die Zimmerbelastung nicht automatisch bestätigen. Das Team vor Ort prüft sie; bitte wenden Sie sich an das Team, falls Ihr Zimmer belastet wurde.",
	},
	"fr": {
		"err.signin":           "Veuillez vous connecter.",
		"err.device.reconnect": "Nous n'avons pas pu détecter votre appareil. Reconnectez-vous au Wi-Fi et réessayez.",
		"err.attempts.moment":  "Trop de tentatives. Veuillez patienter un instant, puis réessayer.",
		"err.free.used":        "Vous avez déjà utilisé votre accès gratuit ici, et aucun autre forfait n'est disponible pour vous pour le moment. Veuillez contacter l'équipe du site.",
		"err.poststay":         "Nous n'avons pas pu vérifier votre séjour. Vérifiez vos informations ou contactez l'équipe du site.",
		"err.timeout.title":    "Cela prend plus de temps que prévu",
		"err.timeout":          "Nous n'avons pas reçu de réponse à temps. Si vous étiez en train de vous connecter, vérifiez si votre appareil est en ligne avant de réessayer.",
		"leave.title":          "Déconnecter cet appareil ?",
		"leave.lead":           "Appuyez sur Déconnecter pour retirer cet appareil du Wi-Fi.",
		"pay.other":            "Choisir un autre moyen de se connecter",
		"room.failed":          "Le débit sur la chambre n'a pas abouti. Aucun montant n'a été facturé pour l'accès Internet. Vous pouvez choisir un autre moyen de vous connecter.",
		"room.review":          "Nous n'avons pas pu confirmer automatiquement le débit sur la chambre. L'équipe du site va le vérifier ; contactez-la si votre chambre a été débitée.",
	},
	"it": {
		"err.signin":           "Accedi.",
		"err.device.reconnect": "Non è stato possibile rilevare il tuo dispositivo. Riconnettiti al Wi-Fi e riprova.",
		"err.attempts.moment":  "Troppi tentativi. Attendi un momento e riprova.",
		"err.free.used":        "Hai già usato l'accesso gratuito qui e al momento non ci sono altri pacchetti disponibili per te. Contatta il personale.",
		"err.poststay":         "Non è stato possibile verificare il tuo soggiorno. Controlla i dati inseriti o contatta il personale.",
		"err.timeout.title":    "Ci vuole più tempo del previsto",
		"err.timeout":          "Non abbiamo ricevuto una risposta in tempo. Se ti stavi connettendo, verifica che il dispositivo sia online prima di riprovare.",
		"leave.title":          "Disconnettere questo dispositivo?",
		"leave.lead":           "Tocca Disconnetti per scollegare questo dispositivo dal Wi-Fi.",
		"pay.other":            "Scegli un altro modo per connetterti",
		"room.failed":          "L'addebito sulla camera non è stato completato. Non è stato addebitato nulla per l'accesso a Internet. Puoi scegliere un altro modo per connetterti.",
		"room.review":          "Non è stato possibile confermare automaticamente l'addebito sulla camera. Il personale lo verificherà; contattalo se la camera è stata addebitata.",
	},
	"ru": {
		"err.signin":           "Пожалуйста, войдите.",
		"err.device.reconnect": "Не удалось определить ваше устройство. Переподключитесь к Wi-Fi и попробуйте снова.",
		"err.attempts.moment":  "Слишком много попыток. Подождите немного и попробуйте снова.",
		"err.free.used":        "Вы уже воспользовались бесплатным доступом здесь, и других пакетов для вас сейчас нет. Обратитесь к персоналу.",
		"err.poststay":         "Не удалось подтвердить ваше пребывание. Проверьте данные или обратитесь к персоналу.",
		"err.timeout.title":    "Это занимает больше времени, чем ожидалось",
		"err.timeout":          "Ответ не получен вовремя. Если вы подключались, сначала проверьте, есть ли у устройства доступ в интернет.",
		"leave.title":          "Отключить это устройство?",
		"leave.lead":           "Нажмите «Отключиться», чтобы отключить это устройство от Wi-Fi.",
		"pay.other":            "Выбрать другой способ подключения",
		"room.failed":          "Списание на номер не выполнено. За доступ в интернет ничего не списано. Вы можете выбрать другой способ подключения.",
		"room.review":          "Не удалось автоматически подтвердить списание на номер. Персонал проверит его; обратитесь к ним, если сумма была списана на номер.",
	},
}

func init() {
	for code, words := range remediationStrings {
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
	serverMessageKeys[msgPleaseSignIn] = "err.signin"
	serverMessageKeys[msgCannotDetectDevice] = "err.device.reconnect"
	serverMessageKeys[msgTooManyAttemptsMoment] = "err.attempts.moment"
	serverMessageKeys[msgFreeUsedNothingElse] = "err.free.used"
	serverMessageKeys[guestPostStayMessage] = "err.poststay"
}
