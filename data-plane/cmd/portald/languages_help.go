package main

// THE HELP SHEET AND THE PRODUCT ATTRIBUTION, IN EVERY SHIPPED LANGUAGE.
//
// The sign-in page used to carry its explanations on the page itself. They now live behind a small lightbulb
// button (help.*) that opens a sheet of tips for the ways in this hotel actually offers; field labels, the
// short hints needed to fill a field, every error and every notice stay on the page. brand.by is the prefix of
// the discreet "Wi-Fi by OneGate" line at the foot of every guest page -- "OneGate" is a product name and is
// never translated, so it is not in this dictionary.
//
// These join builtinStrings at start-up like pageStrings, so there is still one dictionary: the page renders
// it, /api/languages serves it, and Hotel Admin (portal-branding/strings.ts) offers every key for override.
var helpStrings = map[string]map[string]string{
	"en": {
		"brand.by":      "Wi-Fi by",
		"help.button":   "Help and tips",
		"help.title":    "Help with signing in",
		"help.close":    "Close",
		"help.pms":      "Enter your room number, then the detail asked for below it, exactly as it appears on your reservation.",
		"help.poststay": "Already checked out? Enter the PIN you were given at checkout to reconnect.",
		"help.voucher":  "Type the code exactly as it is printed on your voucher, then tap Login.",
		"help.account":  "Enter the username and password you were given. If a voucher field is showing, switch on “Use Personal Account” first.",
		"help.email":    "Enter your email address and tap Send code, then type the 6-digit code from the email. Check your spam folder if it does not arrive.",
		"help.sms":      "Enter your phone number with the country code and tap Send code, then type the 6-digit code from the text message.",
		"help.fail":     "Something not working? Please contact reception — they are happy to help.",
	},
	"ar": {
		"brand.by":      "خدمة الواي فاي من",
		"help.button":   "المساعدة والنصائح",
		"help.title":    "المساعدة في تسجيل الدخول",
		"help.close":    "إغلاق",
		"help.pms":      "أدخل رقم غرفتك، ثم البيان المطلوب أسفله كما يظهر تماماً في حجزك.",
		"help.poststay": "هل غادرت الفندق بالفعل؟ أدخل الرمز الذي تسلّمته عند المغادرة لإعادة الاتصال.",
		"help.voucher":  "اكتب الرمز تماماً كما هو مطبوع على القسيمة، ثم اضغط «تسجيل الدخول».",
		"help.account":  "أدخل اسم المستخدم وكلمة المرور اللذين حصلت عليهما. إذا ظهر حقل القسيمة، فعّل «استخدام حساب شخصي» أولاً.",
		"help.email":    "أدخل بريدك الإلكتروني واضغط «إرسال الرمز»، ثم اكتب الرمز المكوّن من ٦ أرقام الوارد في الرسالة. تحقّق من مجلد الرسائل غير المرغوب فيها إذا لم تصلك.",
		"help.sms":      "أدخل رقم هاتفك مع رمز الدولة واضغط «إرسال الرمز»، ثم اكتب الرمز المكوّن من ٦ أرقام الوارد في الرسالة النصية.",
		"help.fail":     "هل تواجه مشكلة؟ يرجى التواصل مع مكتب الاستقبال، ويسعدنا مساعدتك.",
	},
	"de": {
		"brand.by":      "WLAN von",
		"help.button":   "Hilfe und Tipps",
		"help.title":    "Hilfe bei der Anmeldung",
		"help.close":    "Schließen",
		"help.pms":      "Geben Sie Ihre Zimmernummer ein und darunter die abgefragte Angabe – genau so, wie sie in Ihrer Reservierung steht.",
		"help.poststay": "Bereits ausgecheckt? Geben Sie die PIN ein, die Sie beim Check-out erhalten haben, um sich erneut zu verbinden.",
		"help.voucher":  "Geben Sie den Code genau so ein, wie er auf Ihrem Gutschein steht, und tippen Sie dann auf „Anmelden“.",
		"help.account":  "Geben Sie den Benutzernamen und das Passwort ein, die Sie erhalten haben. Wird ein Gutscheinfeld angezeigt, aktivieren Sie zuerst „Persönliches Konto verwenden“.",
		"help.email":    "Geben Sie Ihre E-Mail-Adresse ein, tippen Sie auf „Code senden“ und geben Sie dann den 6-stelligen Code aus der E-Mail ein. Kommt keine E-Mail an, sehen Sie im Spam-Ordner nach.",
		"help.sms":      "Geben Sie Ihre Telefonnummer mit Ländervorwahl ein, tippen Sie auf „Code senden“ und geben Sie dann den 6-stelligen Code aus der SMS ein.",
		"help.fail":     "Funktioniert etwas nicht? Wenden Sie sich bitte an die Rezeption – wir helfen Ihnen gern.",
	},
	"fr": {
		"brand.by":      "Wi-Fi par",
		"help.button":   "Aide et conseils",
		"help.title":    "Aide à la connexion",
		"help.close":    "Fermer",
		"help.pms":      "Saisissez votre numéro de chambre, puis l'information demandée en dessous, exactement comme elle figure sur votre réservation.",
		"help.poststay": "Vous avez déjà quitté l'hôtel ? Saisissez le code qui vous a été remis au départ pour vous reconnecter.",
		"help.voucher":  "Saisissez le code exactement tel qu'il est imprimé sur votre bon, puis appuyez sur « Se connecter ».",
		"help.account":  "Saisissez le nom d'utilisateur et le mot de passe qui vous ont été remis. Si un champ de code d'accès s'affiche, activez d'abord « Utiliser un compte personnel ».",
		"help.email":    "Saisissez votre adresse e-mail, appuyez sur « Envoyer le code », puis entrez le code à 6 chiffres reçu par e-mail. Vérifiez vos courriers indésirables s'il n'arrive pas.",
		"help.sms":      "Saisissez votre numéro de téléphone avec l'indicatif du pays, appuyez sur « Envoyer le code », puis entrez le code à 6 chiffres reçu par SMS.",
		"help.fail":     "Un problème ? Contactez la réception, nous serons ravis de vous aider.",
	},
	"it": {
		"brand.by":      "Wi-Fi di",
		"help.button":   "Aiuto e suggerimenti",
		"help.title":    "Aiuto per l'accesso",
		"help.close":    "Chiudi",
		"help.pms":      "Inserisci il numero della camera e poi il dato richiesto sotto, esattamente come compare nella prenotazione.",
		"help.poststay": "Hai già effettuato il check-out? Inserisci il PIN ricevuto alla partenza per riconnetterti.",
		"help.voucher":  "Digita il codice esattamente come è stampato sul voucher, poi tocca «Accedi».",
		"help.account":  "Inserisci il nome utente e la password che hai ricevuto. Se vedi il campo del voucher, attiva prima «Usa un account personale».",
		"help.email":    "Inserisci il tuo indirizzo e-mail, tocca «Invia il codice» e digita il codice di 6 cifre ricevuto via e-mail. Se non arriva, controlla la cartella spam.",
		"help.sms":      "Inserisci il numero di telefono con il prefisso internazionale, tocca «Invia il codice» e digita il codice di 6 cifre ricevuto via SMS.",
		"help.fail":     "Qualcosa non funziona? Contatta la reception: saremo lieti di aiutarti.",
	},
	"ru": {
		"brand.by":      "Wi-Fi от",
		"help.button":   "Помощь и советы",
		"help.title":    "Помощь со входом",
		"help.close":    "Закрыть",
		"help.pms":      "Введите номер комнаты, а затем данные, запрошенные под ним, — точно так, как они указаны в брони.",
		"help.poststay": "Уже выехали? Введите PIN, выданный при выезде, чтобы подключиться снова.",
		"help.voucher":  "Введите код точно так, как он напечатан на ваучере, и нажмите «Войти».",
		"help.account":  "Введите имя пользователя и пароль, которые вам выдали. Если отображается поле ваучера, сначала включите «Использовать личный аккаунт».",
		"help.email":    "Введите адрес эл. почты, нажмите «Отправить код» и введите 6-значный код из письма. Если письмо не пришло, проверьте папку «Спам».",
		"help.sms":      "Введите номер телефона с кодом страны, нажмите «Отправить код» и введите 6-значный код из SMS.",
		"help.fail":     "Что-то не работает? Обратитесь на стойку регистрации — мы с радостью поможем.",
	},
}

// The help words join the one dictionary at start-up; a key defined twice is refused loudly, as for pageStrings.
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
