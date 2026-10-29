package main

// The help sheet and product attribution in every shipped language; "OneGate" is a product name and never translated.
var helpStrings = map[string]map[string]string{
	"en": {
		"brand.by":      "Wi-Fi by",
		"help.button":   "Help and tips",
		"help.title":    "Help with signing in",
		"help.close":    "Close",
		"help.pms":      "Enter your room number, then the detail asked for below it, exactly as it appears on your reservation.",
		"help.poststay": "Already checked out? Enter your room number and reservation number to reconnect.",
		"help.voucher":  "Type the code exactly as it is printed on your voucher, then tap Login.",
		"help.account":  "Enter the username and password you were given. If a voucher field is showing, switch on “Use Personal Account” first.",
		"help.sms":      "Enter your phone number with the country code and tap Send code, then type the 6-digit code from the text message.",
		"help.whatsapp": "Enter your WhatsApp phone number with the country code and tap Send code, then type the 6-digit code from the WhatsApp message.",
		"help.fail":     "Something not working? Please contact the site team for assistance.",
	},
	"ar": {
		"brand.by":      "خدمة الواي فاي من",
		"help.button":   "المساعدة والنصائح",
		"help.title":    "المساعدة في تسجيل الدخول",
		"help.close":    "إغلاق",
		"help.pms":      "أدخل رقم غرفتك، ثم البيان المطلوب أسفله كما يظهر تماماً في حجزك.",
		"help.poststay": "هل سجّلت المغادرة بالفعل؟ أدخل رقم غرفتك ورقم الحجز لإعادة الاتصال.",
		"help.voucher":  "اكتب الرمز تماماً كما هو مطبوع على القسيمة، ثم اضغط «تسجيل الدخول».",
		"help.account":  "أدخل اسم المستخدم وكلمة المرور اللذين حصلت عليهما. إذا ظهر حقل القسيمة، فعّل «استخدام حساب شخصي» أولاً.",
		"help.sms":      "أدخل رقم هاتفك مع رمز الدولة واضغط «إرسال الرمز»، ثم اكتب الرمز المكوّن من ٦ أرقام الوارد في الرسالة النصية.",
		"help.whatsapp": "أدخل رقم واتساب مع رمز الدولة واضغط «إرسال الرمز»، ثم اكتب الرمز المكوّن من ٦ أرقام الوارد في رسالة واتساب.",
		"help.fail":     "هل تواجه مشكلة؟ يرجى التواصل مع فريق الموقع للحصول على المساعدة.",
	},
	"de": {
		"brand.by":      "WLAN von",
		"help.button":   "Hilfe und Tipps",
		"help.title":    "Hilfe bei der Anmeldung",
		"help.close":    "Schließen",
		"help.pms":      "Geben Sie Ihre Zimmernummer ein und darunter die abgefragte Angabe – genau so, wie sie in Ihrer Reservierung steht.",
		"help.poststay": "Bereits ausgecheckt? Geben Sie Zimmernummer und Reservierungsnummer ein, um sich erneut zu verbinden.",
		"help.voucher":  "Geben Sie den Code genau so ein, wie er auf Ihrem Gutschein steht, und tippen Sie dann auf „Anmelden“.",
		"help.account":  "Geben Sie den Benutzernamen und das Passwort ein, die Sie erhalten haben. Wird ein Gutscheinfeld angezeigt, aktivieren Sie zuerst „Persönliches Konto verwenden“.",
		"help.sms":      "Geben Sie Ihre Telefonnummer mit Ländervorwahl ein, tippen Sie auf „Code senden“ und geben Sie dann den 6-stelligen Code aus der SMS ein.",
		"help.whatsapp": "Geben Sie Ihre WhatsApp-Nummer mit Ländervorwahl ein, tippen Sie auf „Code senden“ und geben Sie dann den 6-stelligen Code aus der WhatsApp-Nachricht ein.",
		"help.fail":     "Funktioniert etwas nicht? Bitte wenden Sie sich für Unterstützung an das Team vor Ort.",
	},
	"fr": {
		"brand.by":      "Wi-Fi par",
		"help.button":   "Aide et conseils",
		"help.title":    "Aide à la connexion",
		"help.close":    "Fermer",
		"help.pms":      "Saisissez votre numéro de chambre, puis l'information demandée en dessous, exactement comme elle figure sur votre réservation.",
		"help.poststay": "Vous avez déjà effectué votre départ ? Saisissez votre numéro de chambre et votre numéro de réservation pour vous reconnecter.",
		"help.voucher":  "Saisissez le code exactement tel qu'il est imprimé sur votre bon, puis appuyez sur « Se connecter ».",
		"help.account":  "Saisissez le nom d'utilisateur et le mot de passe qui vous ont été remis. Si un champ de code d'accès s'affiche, activez d'abord « Utiliser un compte personnel ».",
		"help.sms":      "Saisissez votre numéro de téléphone avec l'indicatif du pays, appuyez sur « Envoyer le code », puis entrez le code à 6 chiffres reçu par SMS.",
		"help.whatsapp": "Saisissez votre numéro WhatsApp avec l'indicatif du pays, appuyez sur « Envoyer le code », puis entrez le code à 6 chiffres reçu sur WhatsApp.",
		"help.fail":     "Un problème ? Veuillez contacter l'équipe du site pour obtenir de l'aide.",
	},
	"it": {
		"brand.by":      "Wi-Fi di",
		"help.button":   "Aiuto e suggerimenti",
		"help.title":    "Aiuto per l'accesso",
		"help.close":    "Chiudi",
		"help.pms":      "Inserisci il numero della camera e poi il dato richiesto sotto, esattamente come compare nella prenotazione.",
		"help.poststay": "Hai già effettuato il check-out? Inserisci il numero di camera e il numero di prenotazione per riconnetterti.",
		"help.voucher":  "Digita il codice esattamente come è stampato sul voucher, poi tocca «Accedi».",
		"help.account":  "Inserisci il nome utente e la password che hai ricevuto. Se vedi il campo del voucher, attiva prima «Usa un account personale».",
		"help.sms":      "Inserisci il numero di telefono con il prefisso internazionale, tocca «Invia il codice» e digita il codice di 6 cifre ricevuto via SMS.",
		"help.whatsapp": "Inserisci il tuo numero WhatsApp con il prefisso internazionale, tocca «Invia il codice» e digita il codice di 6 cifre ricevuto su WhatsApp.",
		"help.fail":     "Qualcosa non funziona? Contatta il personale della struttura per assistenza.",
	},
	"ru": {
		"brand.by":      "Wi-Fi от",
		"help.button":   "Помощь и советы",
		"help.title":    "Помощь со входом",
		"help.close":    "Закрыть",
		"help.pms":      "Введите номер комнаты, а затем данные, запрошенные под ним, — точно так, как они указаны в брони.",
		"help.poststay": "Уже выехали? Введите номер комнаты и номер бронирования, чтобы подключиться снова.",
		"help.voucher":  "Введите код точно так, как он напечатан на ваучере, и нажмите «Войти».",
		"help.account":  "Введите имя пользователя и пароль, которые вам выдали. Если отображается поле ваучера, сначала включите «Использовать личный аккаунт».",
		"help.sms":      "Введите номер телефона с кодом страны, нажмите «Отправить код» и введите 6-значный код из SMS.",
		"help.whatsapp": "Введите номер WhatsApp с кодом страны, нажмите «Отправить код» и введите 6-значный код из сообщения WhatsApp.",
		"help.fail":     "Что-то не работает? Пожалуйста, обратитесь за помощью к персоналу.",
	},
}

// A key defined twice is refused loudly.
func init() {
	for code, words := range helpStrings {
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
