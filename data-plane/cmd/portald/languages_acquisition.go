package main

// THE WORDS OF PACKAGE ACQUISITION: prices and methods on the package choice, the card payment return page,
// open package selection and its return code. They join the one dictionary at start-up like pageStrings.

var acquisitionStrings = map[string]map[string]string{
	"en": {
		"acq.free":             "Free",
		"acq.connect":          "Connect",
		"acq.card":             "Pay by card",
		"acq.room":             "Charge to my room",
		"pay.title":            "Confirming your payment",
		"pay.lead":             "Keep this page open. We are confirming your payment with the payment provider.",
		"pay.cancelled":        "You left the payment page. If you completed the payment, it will still be confirmed here.",
		"pay.failed":           "The payment was not completed. Nothing was charged for internet access. You can try again.",
		"pay.review":           "We could not confirm your payment automatically. The site team will check it; please contact them if you were charged.",
		"pay.connecting":       "Payment confirmed. Connecting your device…",
		"pay.again":            "Back to sign-in",
		"room.pending":         "We are charging your room. This takes a moment…",
		"open.button":          "Continue without signing in",
		"open.code.ask":        "Have a return code?",
		"open.code.hint":       "Enter the return code you were given to continue with the same access.",
		"open.code.submit":     "Continue",
		"open.code.title":      "Your return code",
		"open.code.lead":       "Keep this code. If this device forgets you, or you use another device, enter it on the sign-in page to continue with the same access.",
		"err.returncode":       "That return code was not recognised. Check it and try again.",
		"err.card.unavailable": "Card payment is not available right now. Please choose another way to get online.",
	},
	"ar": {
		"acq.free":             "مجاني",
		"acq.connect":          "اتصال",
		"acq.card":             "الدفع بالبطاقة",
		"acq.room":             "إضافة المبلغ إلى غرفتي",
		"pay.title":            "جارٍ تأكيد الدفع",
		"pay.lead":             "أبقِ هذه الصفحة مفتوحة. نحن نؤكد عملية الدفع مع مزوّد خدمة الدفع.",
		"pay.cancelled":        "لقد غادرت صفحة الدفع. إذا أتممت الدفع فسيتم تأكيده هنا.",
		"pay.failed":           "لم تكتمل عملية الدفع. لم يتم خصم أي مبلغ مقابل الإنترنت. يمكنك المحاولة مرة أخرى.",
		"pay.review":           "لم نتمكن من تأكيد الدفع تلقائيًا. سيتحقق فريق الموقع من ذلك؛ يرجى التواصل معهم إذا تم خصم المبلغ.",
		"pay.connecting":       "تم تأكيد الدفع. جارٍ توصيل جهازك…",
		"pay.again":            "العودة إلى تسجيل الدخول",
		"room.pending":         "جارٍ إضافة المبلغ إلى غرفتك. يستغرق ذلك لحظة…",
		"open.button":          "المتابعة دون تسجيل الدخول",
		"open.code.ask":        "هل لديك رمز عودة؟",
		"open.code.hint":       "أدخل رمز العودة الذي حصلت عليه للمتابعة بنفس الوصول.",
		"open.code.submit":     "متابعة",
		"open.code.title":      "رمز العودة الخاص بك",
		"open.code.lead":       "احتفظ بهذا الرمز. إذا نسيك هذا الجهاز أو استخدمت جهازًا آخر، أدخله في صفحة تسجيل الدخول للمتابعة بنفس الوصول.",
		"err.returncode":       "لم يتم التعرف على رمز العودة. تحقق منه وحاول مرة أخرى.",
		"err.card.unavailable": "الدفع بالبطاقة غير متاح حاليًا. يرجى اختيار طريقة أخرى للاتصال.",
	},
	"de": {
		"acq.free":             "Kostenlos",
		"acq.connect":          "Verbinden",
		"acq.card":             "Mit Karte bezahlen",
		"acq.room":             "Auf mein Zimmer buchen",
		"pay.title":            "Ihre Zahlung wird bestätigt",
		"pay.lead":             "Lassen Sie diese Seite geöffnet. Wir bestätigen Ihre Zahlung beim Zahlungsanbieter.",
		"pay.cancelled":        "Sie haben die Zahlungsseite verlassen. Falls Sie bezahlt haben, wird die Zahlung hier trotzdem bestätigt.",
		"pay.failed":           "Die Zahlung wurde nicht abgeschlossen. Für den Internetzugang wurde nichts berechnet. Sie können es erneut versuchen.",
		"pay.review":           "Wir konnten Ihre Zahlung nicht automatisch bestätigen. Das Team vor Ort prüft sie; bitte wenden Sie sich an das Team, falls Ihnen etwas berechnet wurde.",
		"pay.connecting":       "Zahlung bestätigt. Ihr Gerät wird verbunden…",
		"pay.again":            "Zurück zur Anmeldung",
		"room.pending":         "Der Betrag wird Ihrem Zimmer belastet. Das dauert einen Moment…",
		"open.button":          "Ohne Anmeldung fortfahren",
		"open.code.ask":        "Haben Sie einen Rückkehrcode?",
		"open.code.hint":       "Geben Sie Ihren Rückkehrcode ein, um mit demselben Zugang fortzufahren.",
		"open.code.submit":     "Weiter",
		"open.code.title":      "Ihr Rückkehrcode",
		"open.code.lead":       "Bewahren Sie diesen Code auf. Wenn dieses Gerät Sie vergisst oder Sie ein anderes Gerät nutzen, geben Sie ihn auf der Anmeldeseite ein, um mit demselben Zugang fortzufahren.",
		"err.returncode":       "Dieser Rückkehrcode wurde nicht erkannt. Prüfen Sie ihn und versuchen Sie es erneut.",
		"err.card.unavailable": "Kartenzahlung ist derzeit nicht verfügbar. Bitte wählen Sie einen anderen Weg ins Internet.",
	},
	"fr": {
		"acq.free":             "Gratuit",
		"acq.connect":          "Se connecter",
		"acq.card":             "Payer par carte",
		"acq.room":             "Porter sur ma chambre",
		"pay.title":            "Confirmation de votre paiement",
		"pay.lead":             "Gardez cette page ouverte. Nous confirmons votre paiement auprès du prestataire de paiement.",
		"pay.cancelled":        "Vous avez quitté la page de paiement. Si vous avez payé, le paiement sera tout de même confirmé ici.",
		"pay.failed":           "Le paiement n'a pas abouti. Aucun montant n'a été facturé pour l'accès Internet. Vous pouvez réessayer.",
		"pay.review":           "Nous n'avons pas pu confirmer votre paiement automatiquement. L'équipe du site va le vérifier ; contactez-la si vous avez été débité.",
		"pay.connecting":       "Paiement confirmé. Connexion de votre appareil…",
		"pay.again":            "Retour à la connexion",
		"room.pending":         "Nous débitons votre chambre. Cela prend un instant…",
		"open.button":          "Continuer sans se connecter",
		"open.code.ask":        "Vous avez un code de retour ?",
		"open.code.hint":       "Saisissez le code de retour qui vous a été remis pour continuer avec le même accès.",
		"open.code.submit":     "Continuer",
		"open.code.title":      "Votre code de retour",
		"open.code.lead":       "Conservez ce code. Si cet appareil vous oublie ou si vous utilisez un autre appareil, saisissez-le sur la page de connexion pour continuer avec le même accès.",
		"err.returncode":       "Ce code de retour n'a pas été reconnu. Vérifiez-le et réessayez.",
		"err.card.unavailable": "Le paiement par carte n'est pas disponible pour le moment. Choisissez un autre moyen de vous connecter.",
	},
	"it": {
		"acq.free":             "Gratuito",
		"acq.connect":          "Connetti",
		"acq.card":             "Paga con carta",
		"acq.room":             "Addebita sulla mia camera",
		"pay.title":            "Conferma del pagamento",
		"pay.lead":             "Tieni aperta questa pagina. Stiamo confermando il pagamento con il fornitore del servizio di pagamento.",
		"pay.cancelled":        "Hai lasciato la pagina di pagamento. Se hai completato il pagamento, verrà comunque confermato qui.",
		"pay.failed":           "Il pagamento non è stato completato. Non è stato addebitato nulla per l'accesso a Internet. Puoi riprovare.",
		"pay.review":           "Non è stato possibile confermare automaticamente il pagamento. Il personale lo verificherà; contattalo se ti è stato addebitato qualcosa.",
		"pay.connecting":       "Pagamento confermato. Connessione del dispositivo…",
		"pay.again":            "Torna all'accesso",
		"room.pending":         "Stiamo addebitando l'importo sulla tua camera. Ci vuole un momento…",
		"open.button":          "Continua senza accedere",
		"open.code.ask":        "Hai un codice di ritorno?",
		"open.code.hint":       "Inserisci il codice di ritorno ricevuto per continuare con lo stesso accesso.",
		"open.code.submit":     "Continua",
		"open.code.title":      "Il tuo codice di ritorno",
		"open.code.lead":       "Conserva questo codice. Se questo dispositivo non ti riconosce più o usi un altro dispositivo, inseriscilo nella pagina di accesso per continuare con lo stesso accesso.",
		"err.returncode":       "Il codice di ritorno non è stato riconosciuto. Controllalo e riprova.",
		"err.card.unavailable": "Il pagamento con carta non è disponibile al momento. Scegli un altro modo per connetterti.",
	},
	"ru": {
		"acq.free":             "Бесплатно",
		"acq.connect":          "Подключиться",
		"acq.card":             "Оплатить картой",
		"acq.room":             "Списать на мой номер",
		"pay.title":            "Подтверждение оплаты",
		"pay.lead":             "Не закрывайте эту страницу. Мы подтверждаем оплату у платёжного провайдера.",
		"pay.cancelled":        "Вы покинули страницу оплаты. Если вы оплатили, платёж всё равно будет подтверждён здесь.",
		"pay.failed":           "Оплата не завершена. За доступ в интернет ничего не списано. Вы можете попробовать снова.",
		"pay.review":           "Не удалось подтвердить оплату автоматически. Персонал проверит её; обратитесь к ним, если средства были списаны.",
		"pay.connecting":       "Оплата подтверждена. Подключаем ваше устройство…",
		"pay.again":            "Вернуться ко входу",
		"room.pending":         "Списываем сумму на ваш номер. Это займёт немного времени…",
		"open.button":          "Продолжить без входа",
		"open.code.ask":        "Есть код возврата?",
		"open.code.hint":       "Введите полученный код возврата, чтобы продолжить с тем же доступом.",
		"open.code.submit":     "Продолжить",
		"open.code.title":      "Ваш код возврата",
		"open.code.lead":       "Сохраните этот код. Если это устройство вас не узнает или вы воспользуетесь другим устройством, введите его на странице входа, чтобы продолжить с тем же доступом.",
		"err.returncode":       "Код возврата не распознан. Проверьте его и попробуйте снова.",
		"err.card.unavailable": "Оплата картой сейчас недоступна. Выберите другой способ подключения.",
	},
}

func init() {
	for code, words := range acquisitionStrings {
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
	serverMessageKeys[msgReturnCodeUnknown] = "err.returncode"
	serverMessageKeys[msgCardUnavailable] = "err.card.unavailable"
}

// The sentences the acquisition handlers compose. Existing sentences are reused where they already say the
// right thing, so they keep their translations.
const (
	msgServiceError       = "Something went wrong. Please try again."
	msgNoDeviceAddress    = "Unable to detect your device address."
	msgDeviceNotOnNetwork = "Your device isn't connected to this Wi-Fi network."
	msgMethodDisabled     = "This sign-in method is not available. Please contact the site team for assistance."
	msgTooManyAttempts    = "Too many attempts. Please wait a minute and try again."
	msgReturnCodeUnknown  = "That return code was not recognised. Check it and try again."
	msgCardUnavailable    = "Card payment is not available right now. Please choose another way to get online."
	msgVoucherInvalid     = "Invalid voucher."
)
